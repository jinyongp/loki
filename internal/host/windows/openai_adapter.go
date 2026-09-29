package windows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	OpenAIProviderID              = "openai"
	OpenAIHelperID                = "openai-tunnel-client"
	openAIAdapterSchemaVersion    = 1
	openAIRuntimeAlias            = "loki"
	openAIProfileName             = "loki"
	openAIRuntimeKeyEnv           = "LOKI_OPENAI_RUNTIME_KEY"
	openAIMCPAuthorizationEnv     = "LOKI_MCP_AUTHORIZATION"
	openAITunnelStateDirectory    = "tunnel-client-state"
	openAITunnelProfileDirectory  = "tunnel-client-profiles"
	openAIAdapterMetadataFileName = "openai.json"
)

const (
	OpenAITunnelsURL     = "https://platform.openai.com/settings/organization/tunnels"
	OpenAIRuntimeKeysURL = "https://platform.openai.com/settings/organization/api-keys"
	OpenAIConnectorsURL  = "https://chatgpt.com/#settings/Connectors"
)

type OpenAISetupConfig struct {
	TunnelID         string
	RuntimeKey       string
	CredentialTarget string
}

type OpenAICredentialStore interface {
	Put(context.Context, string, string) error
	Get(context.Context, string) (string, error)
	Delete(context.Context, string) error
}

type OpenAIHelperRunner interface {
	Run(context.Context, string, []string, map[string]string) (NativeProbe, error)
}

type OpenAILocalConnectionSource interface {
	Read(context.Context, string) (ConnectionMaterial, error)
}

type OpenAIProviderPaths struct {
	Root       string
	Metadata   string
	StateDir   string
	ProfileDir string
}

type OpenAIProviderMetadata struct {
	SchemaVersion    int    `json:"schema_version"`
	TunnelID         string `json:"tunnel_id"`
	CredentialTarget string `json:"credential_target"`
	RuntimeAlias     string `json:"runtime_alias"`
	ProfileName      string `json:"profile_name"`
	LocalOrigin      string `json:"local_origin"`
}

type OpenAIProviderStore interface {
	Ensure(context.Context, string) (OpenAIProviderPaths, error)
	Read(string) (OpenAIProviderMetadata, bool, error)
	Write(context.Context, string, OpenAIProviderMetadata) error
	Cleanup(context.Context, string) error
}

type OpenAIAdapter struct {
	Credentials OpenAICredentialStore
	Runner      OpenAIHelperRunner
	Local       OpenAILocalConnectionSource
	Store       OpenAIProviderStore
	SetupConfig OpenAISetupConfig
}

func (adapter *OpenAIAdapter) Descriptor() ConnectionProviderDescriptor {
	return ConnectionProviderDescriptor{
		ID:          OpenAIProviderID,
		Kind:        ManagedConnectionKind,
		DisplayName: "OpenAI Secure MCP Tunnel",
		Description: "Expose this Loki MCP endpoint through an OpenAI Secure MCP Tunnel.",
		Actions:     []string{"setup", "start", "stop", "remove"},
	}
}

func (adapter *OpenAIAdapter) HelperID() string { return OpenAIHelperID }

func (adapter *OpenAIAdapter) Setup(ctx context.Context, runtime ConnectionRuntimeContext) error {
	if err := adapter.validateDependencies(); err != nil {
		return err
	}
	paths, err := adapter.Store.Ensure(ctx, runtime.Root)
	if err != nil {
		return err
	}
	existing, present, err := adapter.Store.Read(runtime.Root)
	if err != nil {
		return err
	}
	tunnelID := strings.TrimSpace(adapter.SetupConfig.TunnelID)
	if present {
		if err = validateOpenAIMetadata(existing, runtime.Distribution); err != nil {
			return err
		}
		if tunnelID == "" {
			tunnelID = existing.TunnelID
		} else if tunnelID != existing.TunnelID {
			return errors.New("OpenAI managed connection is already bound to a different tunnel; remove it before changing tunnel identity")
		}
	}
	if err = validateOpenAITunnelID(tunnelID); err != nil {
		return err
	}
	target := strings.TrimSpace(adapter.SetupConfig.CredentialTarget)
	if present {
		if target == "" {
			target = existing.CredentialTarget
		} else if target != existing.CredentialTarget {
			return errors.New("OpenAI managed connection is already bound to a different credential target")
		}
	}
	if target == "" {
		target = defaultOpenAICredentialTarget(runtime.Distribution)
	}
	if err = validateOpenAICredentialTarget(target, runtime.Distribution); err != nil {
		return err
	}

	material, err := adapter.Local.Read(ctx, runtime.Distribution)
	if err != nil {
		return fmt.Errorf("read current Loki MCP connection for OpenAI adapter: %w", err)
	}
	key := adapter.SetupConfig.RuntimeKey
	if key == "" {
		key, err = adapter.Credentials.Get(ctx, target)
		if err != nil {
			return fmt.Errorf("read OpenAI runtime key from Windows Credential Manager: %w", err)
		}
	}
	if err = validateRuntimeSecret(key); err != nil {
		return err
	}

	metadata := OpenAIProviderMetadata{
		SchemaVersion: openAIAdapterSchemaVersion,
		TunnelID:      tunnelID, CredentialTarget: target,
		RuntimeAlias: openAIRuntimeAlias, ProfileName: openAIProfileName,
		LocalOrigin: material.LocalOrigin,
	}
	if present {
		if err = adapter.stopNative(ctx, runtime, paths); err != nil {
			return fmt.Errorf("stop existing OpenAI tunnel runtime before setup reconciliation: %w", err)
		}
	}
	if err = adapter.connect(ctx, runtime, paths, metadata, material, key); err != nil {
		return err
	}
	if err = adapter.doctor(ctx, runtime, paths, metadata, material, key); err != nil {
		_ = adapter.stopNative(ctx, runtime, paths)
		return err
	}
	if adapter.SetupConfig.RuntimeKey != "" {
		if err = adapter.Credentials.Put(ctx, target, adapter.SetupConfig.RuntimeKey); err != nil {
			_ = adapter.stopNative(ctx, runtime, paths)
			return fmt.Errorf("store OpenAI runtime key in Windows Credential Manager: %w", err)
		}
	}
	if err = adapter.Store.Write(ctx, runtime.Root, metadata); err != nil {
		_ = adapter.stopNative(ctx, runtime, paths)
		return fmt.Errorf("persist OpenAI managed connection metadata: %w", err)
	}
	return nil
}

func (adapter *OpenAIAdapter) Start(ctx context.Context, runtime ConnectionRuntimeContext) error {
	if err := adapter.validateDependencies(); err != nil {
		return err
	}
	paths, metadata, material, key, err := adapter.runtimeInputs(ctx, runtime)
	if err != nil {
		return err
	}
	if err = adapter.stopNative(ctx, runtime, paths); err != nil {
		return fmt.Errorf("stop OpenAI tunnel runtime before restart: %w", err)
	}
	if err = adapter.connect(ctx, runtime, paths, metadata, material, key); err != nil {
		return err
	}
	if err = adapter.doctor(ctx, runtime, paths, metadata, material, key); err != nil {
		_ = adapter.stopNative(ctx, runtime, paths)
		return err
	}
	if metadata.LocalOrigin != material.LocalOrigin {
		metadata.LocalOrigin = material.LocalOrigin
		if err = adapter.Store.Write(ctx, runtime.Root, metadata); err != nil {
			_ = adapter.stopNative(ctx, runtime, paths)
			return fmt.Errorf("refresh OpenAI local-origin metadata: %w", err)
		}
	}
	return nil
}

func (adapter *OpenAIAdapter) Status(ctx context.Context, runtime ConnectionRuntimeContext) (ConnectionRuntimeStatus, error) {
	if err := adapter.validateDependencies(); err != nil {
		return ConnectionRuntimeStatus{}, err
	}
	paths, err := adapter.Store.Ensure(ctx, runtime.Root)
	if err != nil {
		return ConnectionRuntimeStatus{}, err
	}
	metadata, present, err := adapter.Store.Read(runtime.Root)
	if err != nil {
		return ConnectionRuntimeStatus{}, err
	}
	if !present {
		return ConnectionRuntimeStatus{}, errors.New("OpenAI managed connection metadata is missing")
	}
	if err = validateOpenAIMetadata(metadata, runtime.Distribution); err != nil {
		return ConnectionRuntimeStatus{}, err
	}
	probe, err := adapter.Runner.Run(ctx, runtime.Helper.ExecutablePath,
		[]string{"runtimes", "status", metadata.RuntimeAlias, "--json"},
		openAIBaseEnvironment(paths),
	)
	if err != nil {
		return ConnectionRuntimeStatus{}, err
	}
	if probe.ExitCode != 0 {
		return ConnectionRuntimeStatus{}, openAIProcessFailure("inspect OpenAI tunnel runtime", probe)
	}
	return parseOpenAIRuntimeStatus(probe.Stdout)
}

func (adapter *OpenAIAdapter) Stop(ctx context.Context, runtime ConnectionRuntimeContext) error {
	if err := adapter.validateDependencies(); err != nil {
		return err
	}
	paths, err := adapter.Store.Ensure(ctx, runtime.Root)
	if err != nil {
		return err
	}
	metadata, present, err := adapter.Store.Read(runtime.Root)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	if err = validateOpenAIMetadata(metadata, runtime.Distribution); err != nil {
		return err
	}
	return adapter.stopNative(ctx, runtime, paths)
}

func (adapter *OpenAIAdapter) Remove(ctx context.Context, runtime ConnectionRuntimeContext) error {
	if err := adapter.validateDependencies(); err != nil {
		return err
	}
	paths, err := adapter.Store.Ensure(ctx, runtime.Root)
	if err != nil {
		return err
	}
	metadata, present, err := adapter.Store.Read(runtime.Root)
	if err != nil {
		return err
	}
	if present {
		if err = validateOpenAIMetadata(metadata, runtime.Distribution); err != nil {
			return err
		}
		if err = adapter.stopNative(ctx, runtime, paths); err != nil {
			return err
		}
		probe, runErr := adapter.Runner.Run(ctx, runtime.Helper.ExecutablePath,
			[]string{"runtimes", "rm", metadata.RuntimeAlias, "--json"},
			openAIBaseEnvironment(paths),
		)
		if runErr != nil {
			return runErr
		}
		if probe.ExitCode != 0 {
			return openAIProcessFailure("remove local OpenAI tunnel runtime metadata", probe)
		}
		if err = adapter.Credentials.Delete(ctx, metadata.CredentialTarget); err != nil {
			return fmt.Errorf("remove OpenAI runtime key from Windows Credential Manager: %w", err)
		}
	} else {
		target := strings.TrimSpace(adapter.SetupConfig.CredentialTarget)
		if target == "" {
			target = defaultOpenAICredentialTarget(runtime.Distribution)
		}
		if validateOpenAICredentialTarget(target, runtime.Distribution) == nil {
			_ = adapter.Credentials.Delete(ctx, target)
		}
	}
	return adapter.Store.Cleanup(ctx, runtime.Root)
}

func (adapter *OpenAIAdapter) runtimeInputs(
	ctx context.Context,
	runtime ConnectionRuntimeContext,
) (OpenAIProviderPaths, OpenAIProviderMetadata, ConnectionMaterial, string, error) {
	paths, err := adapter.Store.Ensure(ctx, runtime.Root)
	if err != nil {
		return OpenAIProviderPaths{}, OpenAIProviderMetadata{}, ConnectionMaterial{}, "", err
	}
	metadata, present, err := adapter.Store.Read(runtime.Root)
	if err != nil {
		return OpenAIProviderPaths{}, OpenAIProviderMetadata{}, ConnectionMaterial{}, "", err
	}
	if !present {
		return OpenAIProviderPaths{}, OpenAIProviderMetadata{}, ConnectionMaterial{}, "",
			errors.New("OpenAI managed connection metadata is missing")
	}
	if err = validateOpenAIMetadata(metadata, runtime.Distribution); err != nil {
		return OpenAIProviderPaths{}, OpenAIProviderMetadata{}, ConnectionMaterial{}, "", err
	}
	material, err := adapter.Local.Read(ctx, runtime.Distribution)
	if err != nil {
		return OpenAIProviderPaths{}, OpenAIProviderMetadata{}, ConnectionMaterial{}, "",
			fmt.Errorf("read current Loki MCP connection for OpenAI adapter: %w", err)
	}
	key, err := adapter.Credentials.Get(ctx, metadata.CredentialTarget)
	if err != nil {
		return OpenAIProviderPaths{}, OpenAIProviderMetadata{}, ConnectionMaterial{}, "",
			fmt.Errorf("OpenAI runtime credential is unavailable: %w", err)
	}
	if err = validateRuntimeSecret(key); err != nil {
		return OpenAIProviderPaths{}, OpenAIProviderMetadata{}, ConnectionMaterial{}, "", err
	}
	return paths, metadata, material, key, nil
}

func (adapter *OpenAIAdapter) connect(
	ctx context.Context,
	runtime ConnectionRuntimeContext,
	paths OpenAIProviderPaths,
	metadata OpenAIProviderMetadata,
	material ConnectionMaterial,
	runtimeKey string,
) error {
	environment := openAIRuntimeEnvironment(paths, runtimeKey, material.Token)
	arguments := []string{
		"runtimes", "connect",
		"--alias", metadata.RuntimeAlias,
		"--tunnel-id", metadata.TunnelID,
		"--profile", metadata.ProfileName,
		"--profile-dir", paths.ProfileDir,
		"--runtime-api-key", "env:" + openAIRuntimeKeyEnv,
		"--mcp-server-url", material.LocalOrigin,
		"--json",
	}
	probe, err := adapter.Runner.Run(ctx, runtime.Helper.ExecutablePath, arguments, environment)
	if err != nil {
		return err
	}
	if probe.ExitCode != 0 {
		return openAIProcessFailure("connect OpenAI tunnel runtime", redactOpenAIProbe(probe, runtimeKey, material.Token))
	}
	return nil
}

func (adapter *OpenAIAdapter) doctor(
	ctx context.Context,
	runtime ConnectionRuntimeContext,
	paths OpenAIProviderPaths,
	metadata OpenAIProviderMetadata,
	material ConnectionMaterial,
	runtimeKey string,
) error {
	probe, err := adapter.Runner.Run(ctx, runtime.Helper.ExecutablePath,
		[]string{"doctor", "--profile", metadata.ProfileName, "--json"},
		openAIRuntimeEnvironment(paths, runtimeKey, material.Token),
	)
	if err != nil {
		return err
	}
	if probe.ExitCode != 0 {
		return openAIDoctorFailure(redactOpenAIProbe(probe, runtimeKey, material.Token))
	}
	return nil
}

func (adapter *OpenAIAdapter) stopNative(
	ctx context.Context,
	runtime ConnectionRuntimeContext,
	paths OpenAIProviderPaths,
) error {
	probe, err := adapter.Runner.Run(ctx, runtime.Helper.ExecutablePath,
		[]string{"runtimes", "stop", openAIRuntimeAlias, "--json"},
		openAIBaseEnvironment(paths),
	)
	if err != nil {
		return err
	}
	if probe.ExitCode != 0 {
		return openAIProcessFailure("stop OpenAI tunnel runtime", probe)
	}
	return nil
}

func (adapter *OpenAIAdapter) validateDependencies() error {
	if adapter.Credentials == nil || adapter.Runner == nil || adapter.Local == nil || adapter.Store == nil {
		return errors.New("OpenAI managed connection adapter is incomplete")
	}
	return nil
}

func openAIBaseEnvironment(paths OpenAIProviderPaths) map[string]string {
	return map[string]string{
		"TUNNEL_CLIENT_STATE_DIR":   paths.StateDir,
		"TUNNEL_CLIENT_PROFILE_DIR": paths.ProfileDir,
	}
}

func openAIRuntimeEnvironment(paths OpenAIProviderPaths, runtimeKey, token string) map[string]string {
	environment := openAIBaseEnvironment(paths)
	environment[openAIRuntimeKeyEnv] = runtimeKey
	environment[openAIMCPAuthorizationEnv] = "Bearer " + token
	environment["MCP_EXTRA_HEADERS"] = "Authorization: env:" + openAIMCPAuthorizationEnv
	return environment
}

func parseOpenAIRuntimeStatus(raw string) (ConnectionRuntimeStatus, error) {
	var payload struct {
		RuntimeState   string `json:"runtime_state"`
		ProcessRunning bool   `json:"process_running"`
		Healthy        bool   `json:"healthy"`
		Ready          bool   `json:"ready"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&payload); err != nil {
		return ConnectionRuntimeStatus{}, fmt.Errorf("decode OpenAI tunnel runtime status: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return ConnectionRuntimeStatus{}, err
	}
	state := strings.TrimSpace(payload.RuntimeState)
	if state == "" {
		return ConnectionRuntimeStatus{}, errors.New("OpenAI tunnel runtime status is missing runtime_state")
	}
	healthy := payload.ProcessRunning && payload.Healthy
	return ConnectionRuntimeStatus{
		State: state, Healthy: healthy, Ready: healthy && payload.Ready,
	}, nil
}

func encodeOpenAIMetadata(metadata OpenAIProviderMetadata) ([]byte, error) {
	if err := validateOpenAIMetadata(metadata, ""); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func parseOpenAIMetadata(raw []byte) (OpenAIProviderMetadata, error) {
	var metadata OpenAIProviderMetadata
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return OpenAIProviderMetadata{}, fmt.Errorf("decode OpenAI managed connection metadata: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return OpenAIProviderMetadata{}, err
	}
	if err := validateOpenAIMetadata(metadata, ""); err != nil {
		return OpenAIProviderMetadata{}, err
	}
	return metadata, nil
}

func validateOpenAIMetadata(metadata OpenAIProviderMetadata, distribution string) error {
	if metadata.SchemaVersion != openAIAdapterSchemaVersion ||
		validateOpenAITunnelID(metadata.TunnelID) != nil ||
		metadata.RuntimeAlias != openAIRuntimeAlias ||
		metadata.ProfileName != openAIProfileName ||
		strings.TrimSpace(metadata.LocalOrigin) == "" {
		return errors.New("OpenAI managed connection metadata is invalid")
	}
	if distribution != "" {
		if err := validateOpenAICredentialTarget(metadata.CredentialTarget, distribution); err != nil {
			return err
		}
	} else if !strings.HasPrefix(metadata.CredentialTarget, "Loki/OpenAI/") ||
		strings.ContainsAny(metadata.CredentialTarget, "\r\n\x00") {
		return errors.New("OpenAI managed connection credential target is invalid")
	}
	if _, _, err := parseLoopbackOrigin(metadata.LocalOrigin, false); err != nil {
		return errors.New("OpenAI managed connection local origin is invalid")
	}
	return nil
}

func validateOpenAITunnelID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("OpenAI tunnel id is invalid")
	}
	return nil
}

func defaultOpenAICredentialTarget(distribution string) string {
	return "Loki/OpenAI/" + distribution + "/runtime-api-key"
}

func validateOpenAICredentialTarget(target, distribution string) error {
	if err := ValidateDistributionName(distribution); err != nil {
		return err
	}
	if target != defaultOpenAICredentialTarget(distribution) ||
		strings.ContainsAny(target, "\r\n\x00") {
		return errors.New("OpenAI runtime credential target is not the Loki-owned target for this distribution")
	}
	return nil
}

func validateRuntimeSecret(secret string) error {
	if secret == "" || len(secret) > 4096 || strings.ContainsAny(secret, "\r\n\x00") {
		return errors.New("OpenAI runtime credential is invalid")
	}
	return nil
}

func redactOpenAIProbe(probe NativeProbe, secrets ...string) NativeProbe {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		probe.Stdout = strings.ReplaceAll(probe.Stdout, secret, "[REDACTED]")
		probe.Stderr = strings.ReplaceAll(probe.Stderr, secret, "[REDACTED]")
		probe.Stdout = strings.ReplaceAll(probe.Stdout, "Bearer "+secret, "Bearer [REDACTED]")
		probe.Stderr = strings.ReplaceAll(probe.Stderr, "Bearer "+secret, "Bearer [REDACTED]")
	}
	return probe
}

type openAIDoctorReport struct {
	FailedChecks []string            `json:"failed_checks"`
	Checks       []openAIDoctorCheck `json:"checks"`
}

type openAIDoctorCheck struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

func openAIDoctorFailure(probe NativeProbe) error {
	for _, raw := range []string{probe.Stdout, probe.Stderr} {
		var report openAIDoctorReport
		if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &report); err != nil ||
			len(report.FailedChecks) == 0 {
			continue
		}
		checks := make(map[string]string, len(report.Checks))
		for _, check := range report.Checks {
			if summary := strings.Join(strings.Fields(check.Summary), " "); summary != "" {
				checks[check.ID] = summary
			}
		}
		details := make([]string, 0, len(report.FailedChecks))
		for _, id := range report.FailedChecks {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if summary := checks[id]; summary != "" {
				details = append(details, id+": "+summary)
			} else {
				details = append(details, id)
			}
		}
		if len(details) != 0 {
			return fmt.Errorf("OpenAI tunnel doctor failed: %s", strings.Join(details, "; "))
		}
	}
	return openAIProcessFailure("run OpenAI tunnel doctor", probe)
}

func openAIProcessFailure(operation string, probe NativeProbe) error {
	detail := strings.TrimSpace(probe.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(probe.Stdout)
	}
	if detail == "" {
		return fmt.Errorf("%s failed with exit code %d", operation, probe.ExitCode)
	}
	return fmt.Errorf("%s failed with exit code %d: %s", operation, probe.ExitCode, detail)
}
