package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type LiveReplicaSource interface {
	Status(context.Context, string) (OperatorStatus, error)
	Connection(context.Context, string) (ConnectionMaterial, error)
}

type ReplicaSnapshot struct {
	Present     bool
	LocalOrigin string
	TokenSHA256 string
}

func BuildReplicaSnapshot(
	expected ExpectedInstallation,
	state WindowsState,
	connectionRaw, tokenRaw []byte,
) (ReplicaSnapshot, error) {
	connectionState, err := ParseLegacyConnection(connectionRaw, expected)
	if err != nil {
		return ReplicaSnapshot{}, fmt.Errorf("validate Windows connection replica: %w", err)
	}
	if state.MCPPort > 0 && state.MCPPort != connectionState.MCPPort {
		return ReplicaSnapshot{}, errors.New("Windows connection replica port does not match verified ownership")
	}
	token := string(tokenRaw)
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n\x00") {
		return ReplicaSnapshot{}, errors.New("Windows MCP token replica is invalid")
	}
	sum := sha256.Sum256(tokenRaw)
	return ReplicaSnapshot{
		Present:     true,
		LocalOrigin: fmt.Sprintf("http://127.0.0.1:%d/mcp", connectionState.MCPPort),
		TokenSHA256: hex.EncodeToString(sum[:]),
	}, nil
}

type ReplicaStore interface {
	Read(ExpectedInstallation) (ReplicaSnapshot, error)
	Publish(context.Context, ExpectedInstallation, ConnectionMaterial) error
}

type ConnectionRuntimeReconciler interface {
	ReconcileEnabled(context.Context, string) error
}

type ReplicaSyncResult struct {
	Changed bool
	Status  OperatorStatus
}

type ReplicaSynchronizer struct {
	Source     LiveReplicaSource
	Store      ReplicaStore
	Reconciler ConnectionRuntimeReconciler
}

func (syncer ReplicaSynchronizer) Sync(
	ctx context.Context,
	expected ExpectedInstallation,
) (ReplicaSyncResult, error) {
	if syncer.Source == nil || syncer.Store == nil {
		return ReplicaSyncResult{}, errors.New("Windows connection replica synchronizer is incomplete")
	}
	status, err := syncer.Source.Status(ctx, expected.Distribution)
	if err != nil {
		return ReplicaSyncResult{}, err
	}
	material, err := syncer.Source.Connection(ctx, expected.Distribution)
	if err != nil {
		return ReplicaSyncResult{}, err
	}
	current, err := syncer.Store.Read(expected)
	if err != nil {
		return ReplicaSyncResult{}, err
	}
	sum := sha256.Sum256([]byte(material.Token))
	nextDigest := hex.EncodeToString(sum[:])
	changed := !current.Present || current.LocalOrigin != material.LocalOrigin || current.TokenSHA256 != nextDigest
	if err = syncer.Store.Publish(ctx, expected, material); err != nil {
		return ReplicaSyncResult{}, err
	}
	// Lifecycle operations can stop a tunnel while leaving its endpoint and
	// token unchanged. Restore enabled connections after every successful sync.
	if syncer.Reconciler != nil {
		if err = syncer.Reconciler.ReconcileEnabled(ctx, expected.Distribution); err != nil {
			return ReplicaSyncResult{}, err
		}
	}
	return ReplicaSyncResult{Changed: changed, Status: status}, nil
}

type WSLLiveReplicaSource struct {
	Operator OperatorClient
}

func (source WSLLiveReplicaSource) Status(ctx context.Context, distribution string) (OperatorStatus, error) {
	result, err := source.Operator.Execute(ctx, distribution, OperatorRequest{Command: "status"})
	if err != nil {
		return OperatorStatus{}, err
	}
	if result.Probe.ExitCode != 0 {
		return OperatorStatus{}, nativeFailure("read Loki host status", result.Probe)
	}
	return ParseOperatorStatus([]byte(result.Probe.Stdout))
}

func (source WSLLiveReplicaSource) Connection(ctx context.Context, distribution string) (ConnectionMaterial, error) {
	result, err := source.Operator.Execute(ctx, distribution, OperatorRequest{Command: "connection"})
	if err != nil {
		return ConnectionMaterial{}, err
	}
	if result.Probe.ExitCode != 0 {
		return ConnectionMaterial{}, nativeFailure("read Loki connection information", result.Probe)
	}
	material, err := parseLiveConnection([]byte(result.Probe.Stdout))
	if err != nil {
		return ConnectionMaterial{}, err
	}
	identityBefore, err := source.Operator.WSL.OwnedDistributionVersion(ctx, distribution)
	if err != nil {
		return ConnectionMaterial{}, err
	}
	tokenResult, err := source.Operator.WSL.run(ctx, "-d", distribution, "--user", "root", "--exec",
		"/bin/cat", "/var/lib/loki/lifecycle/mcp-token")
	if err != nil {
		return ConnectionMaterial{}, err
	}
	if tokenResult.ExitCode != 0 {
		return ConnectionMaterial{}, nativeFailure("read Loki MCP token", tokenResult)
	}
	identityAfter, err := source.Operator.WSL.OwnedDistributionVersion(ctx, distribution)
	if err != nil {
		return ConnectionMaterial{}, err
	}
	if identityAfter != identityBefore {
		return ConnectionMaterial{}, errors.New("Loki appliance identity changed while reading connection material")
	}
	token := strings.TrimSpace(tokenResult.Stdout)
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return ConnectionMaterial{}, errors.New("Loki MCP token material is invalid")
	}
	material.Token = token
	return material, nil
}

func parseLiveConnection(raw []byte) (ConnectionMaterial, error) {
	if err := ValidateOperatorInfoSchema(raw); err != nil {
		return ConnectionMaterial{}, err
	}
	var connection struct {
		SchemaVersion int `json:"schema_version"`
		LocalOrigin   struct {
			URL            string `json:"url"`
			Transport      string `json:"transport"`
			Reachability   string `json:"reachability"`
			Authentication struct {
				Type string `json:"type"`
			} `json:"authentication"`
		} `json:"local_origin"`
	}
	if err := json.Unmarshal(raw, &connection); err != nil {
		return ConnectionMaterial{}, fmt.Errorf("decode Loki connection information: %w", err)
	}
	if connection.SchemaVersion != OperatorInfoSchemaVersion ||
		connection.LocalOrigin.Transport != "streamable-http" ||
		connection.LocalOrigin.Reachability != "loopback" ||
		connection.LocalOrigin.Authentication.Type != "bearer-token-file" {
		return ConnectionMaterial{}, errors.New("Loki connection information does not match Windows frontend contract")
	}
	origin, _, err := parseLoopbackOrigin(connection.LocalOrigin.URL, false)
	if err != nil {
		return ConnectionMaterial{}, err
	}
	return ConnectionMaterial{
		LocalOrigin:        origin,
		Transport:          connection.LocalOrigin.Transport,
		Reachability:       connection.LocalOrigin.Reachability,
		AuthenticationType: connection.LocalOrigin.Authentication.Type,
	}, nil
}
