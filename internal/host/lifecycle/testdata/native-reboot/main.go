// Disposable VM peer. It uses the production clients and kernel identities;
// no secret is accepted as an argument, environment variable or public output.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"loki/internal/admin"
	"loki/internal/config"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/execution"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/platform/repository"
	"loki/internal/rpc"
	"loki/modules/execution/jobs"
	"loki/modules/execution/jobs/remote"
)

const requestID = "123e4567-e89b-42d3-a456-426614174391"

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	if err := run(ctx, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "native owned fixture:", err)
		os.Exit(1)
	}
}

func writePublic(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0640); err != nil {
		return err
	}
	if err := os.Chmod(path, 0640); err != nil {
		return err
	}
	return os.Chown(path, 0, 21001)
}

func run(ctx context.Context, action string) error {
	switch action {
	case "configure":
		if os.Geteuid() != 0 {
			return errors.New("fixture configuration requires root")
		}
		r, err := repository.New("/srv/workspace/loki")
		if err != nil {
			return err
		}
		defer r.Close()
		id, err := r.Identity(".")
		if err != nil {
			return err
		}
		resources := []controlpolicy.ApplicationResource{{ID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", Alias: "fixture", Repository: id, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Network: "none", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true, Write: true, Execute: true}}}}
		c, err := config.Load("/etc/loki-go/config.toml")
		if err != nil {
			return err
		}
		raw, err := os.ReadFile("/usr/share/doc/loki/execution-contract.json")
		if err != nil {
			return err
		}
		contract, err := execution.Load(raw)
		if err != nil {
			return err
		}
		policy, err := hostpolicy.CompileApplications(c, contract, resources)
		if err != nil {
			return err
		}
		if err := writePublic("/etc/loki-go/application-resources.json", map[string]any{"resources": resources}); err != nil {
			return err
		}
		return writePublic("/etc/loki-go/fixture-policy.json", map[string]string{"policy": policy.Digest()})
	case "seed":
		var layout struct {
			Socket string `json:"socket"`
			UID    uint32 `json:"uid"`
		}
		if err := daemon.ReadJSON("/etc/loki-go/secrets-admin.json", &layout); err != nil {
			return err
		}
		client := admin.ApplicationClient{Socket: layout.Socket, ExpectedUID: layout.UID}
		raw, err := client.Call(ctx, appvalues.AdminRequest{Action: "revision", Resource: "fixture"}, nil)
		if err != nil {
			return err
		}
		var result struct {
			Revision string `json:"revision"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Revision == "" {
			return errors.New("missing edit revision")
		}
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		value := []byte(base64.RawURLEncoding.EncodeToString(nonce[:]))
		clear(nonce[:])
		defer clear(value)
		_, err = client.Call(ctx, appvalues.AdminRequest{Action: "set_secret", Resource: "fixture", Name: "TOKEN", Revision: result.Revision, RequestID: "123e4567-e89b-42d3-a456-426614174392"}, value)
		return err
	case "coordination":
		if os.Geteuid() != 21000 {
			return errors.New("coordination fixture must use the real agent UID")
		}
		root := uint32(0)
		raw, err := (rpc.Client{Socket: "/run/loki-go/runtime/control.sock", ExpectedUID: &root}).Call(ctx, map[string]any{"operation": "devtools_workstream_list", "cwd": ".", "limit": 10})
		if err != nil {
			return err
		}
		var result struct {
			Profile string `json:"profile"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Profile != "native-fixture" {
			return errors.New("coordination did not return the independent fixture profile")
		}
		return nil
	case "start", "check", "generic":
		if os.Geteuid() != 21004 {
			return errors.New("Job fixture must use the real executor UID")
		}
		var metadata struct {
			Policy string `json:"policy"`
		}
		if err := daemon.ReadJSON("/etc/loki-go/fixture-policy.json", &metadata); err != nil {
			return err
		}
		root := uint32(0)
		client, err := remote.New(remote.Options{Socket: "/run/loki-go/launcher/control.sock", ExpectedUID: &root, PolicySHA256: metadata.Policy, Timeout: 30 * time.Second})
		if err != nil {
			return err
		}
		input := jobs.StartRequest{RequestID: requestID, CWD: ".", Argv: []string{"/bin/sh", "-c", "printf '%s' \"$TOKEN\"; sleep 300"}, Resource: "fixture", Environment: "common", TimeoutSeconds: 600}
		if action == "generic" {
			input = jobs.StartRequest{RequestID: "123e4567-e89b-42d3-a456-426614174393", CWD: ".", Argv: []string{"/bin/sh", "-c", "test -z \"${TOKEN+x}\" && printf native-generic-ready"}, TimeoutSeconds: 60}
		}
		req, fingerprint, err := jobs.NormalizeStartRequest(input)
		if err != nil {
			return err
		}
		id, err := jobs.JobIDForRequestID(req.RequestID)
		if err != nil {
			return err
		}
		result, err := client.Start(ctx, jobs.Workload{ID: id, RequestID: req.RequestID, RequestSHA256: fingerprint, CWD: req.CWD, Argv: req.Argv, Network: req.Network, TimeoutSeconds: req.TimeoutSeconds, Resource: req.Resource, Environment: req.Environment})
		if err != nil || action == "check" && !result.Replayed || action != "check" && result.Replayed {
			return fmt.Errorf("unexpected replay state: %v", err)
		}
		for {
			status, err := client.Inspect(ctx, id)
			if err != nil {
				return err
			}
			if action == "start" && status.State == jobs.StateRunning {
				return nil
			}
			if action == "check" && status.State == jobs.StateTerminal && (status.Cleanup == jobs.CleanupComplete || status.Cleanup == jobs.CleanupNotRequired) {
				return nil
			}
			if action == "generic" && status.State == jobs.StateTerminal {
				output, err := client.Output(ctx, id)
				if err != nil || status.Outcome != jobs.OutcomeExited || status.ExitCode == nil || *status.ExitCode != 0 || status.Cleanup != jobs.CleanupComplete || output.Output != "native-generic-ready" || !output.Complete || output.Truncated || output.OutputGap != nil {
					return errors.New("generic Job did not complete independently without injected app values")
				}
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	case "journal":
		items, err := jobs.ReadJournalSnapshot("/var/lib/loki-go/launcher/journal", jobs.JournalLimits{MaxRecords: 64, MaxRecordBytes: jobs.JournalRecordBudget(262144), MaxOutputBytes: 262144, Retention: time.Hour})
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.RequestID == requestID && item.Protected != nil && strings.Contains(item.Protected.Authority.ResourceID, "9af6c2e6") {
				return nil
			}
		}
		return errors.New("actual protected Job was not durably recorded")
	default:
		return errors.New("unsupported fixture action")
	}
}
