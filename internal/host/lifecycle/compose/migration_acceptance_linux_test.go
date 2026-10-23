//go:build linux

package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

func TestRealComposeMigrationPublicLifecycle(t *testing.T) {
	image := os.Getenv("LOKI_TEST_NATIVE_SECRET_IMAGE")
	if image == "" {
		if os.Getenv("LOKI_REQUIRE_NATIVE_SECRET_COMPOSE") == "1" {
			t.Fatal("actual pinned image is required")
		}
		t.Skip("native package suite supplies actual Docker services")
	}
	binary := os.Getenv("LOKI_TEST_HOST_MIGRATION_CLI")
	if !filepath.IsAbs(binary) {
		t.Fatal("actual public host CLI is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	sum := sha256.Sum256([]byte(root))
	b, err := New(Config{StateRoot: filepath.Join(root, "operator"), Project: "loki-migrate-" + hex.EncodeToString(sum[:6]), Runner: ExecRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	state := runtimeState{Version: 1, GenerationID: "sha256:" + strings.Repeat("a", 64), CoreImage: image, Workspace: workspace, MCPPort: port}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		if current, exists, _ := b.loadRuntime(); exists {
			state = current
		}
		if t.Failed() {
			// This owned fixture runs no project command and contains only
			// synthetic data. Capture bounded startup diagnostics before cleanup.
			if body, err := b.compose(cleanup, state, "logs", "--no-color", "--tail", "20", "runtime", "launcher", "executor", "application-values"); err == nil {
				t.Log("finite owned service startup diagnostics:\n" + string(body))
			}
		}
		if _, err := b.compose(cleanup, state, "down", "--volumes", "--remove-orphans"); err != nil {
			t.Error("owned services cleanup", err)
		}
		ids, err := b.runner.Run(cleanup, nil, "container", "ls", "--all", "--filter", "label=io.loki.migration.owner="+applicationOwnerID(b.root), "--format", "{{.ID}}")
		if err != nil {
			t.Error(err)
		}
		for _, id := range strings.Fields(string(ids)) {
			if _, err := b.runner.Run(cleanup, nil, "container", "rm", "--force", id); err != nil {
				t.Error(err)
			}
		}
		for _, name := range []string{"application-state", "runtime-state"} {
			if found, _ := b.volumeExists(cleanup, b.volumeName(name)); found {
				if _, err := b.runner.Run(cleanup, nil, "volume", "rm", b.volumeName(name)); err != nil {
					t.Error(err)
				}
			}
		}
		if state.ApplicationRuntimeRoot != "" {
			_, err := b.runner.(PrivateRunner).RunPrivate(cleanup, nil, nil, "run", "--rm", "--network", "none", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--user", "0:0", "--mount", applicationBind(state.ApplicationRuntimeRoot, "/fixture", false), "--entrypoint", "/bin/sh", image, "-ec", "find /fixture -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +")
			if err != nil {
				t.Error(err)
			}
			if err := os.Remove(state.ApplicationRuntimeRoot); err != nil {
				t.Error(err)
			}
			if err := os.Remove(filepath.Dir(state.ApplicationRuntimeRoot)); err != nil {
				t.Error(err)
			}
		}
	})
	if err := b.prepareApplicationValues(ctx, &state); err != nil {
		t.Fatal("initial normal roles", err)
	}
	if err := b.saveRuntime(state); err != nil {
		t.Fatal(err)
	}
	if _, err := b.compose(ctx, state, "up", "--detach", "--wait", "--wait-timeout", "45", "application-values", "launcher"); err != nil {
		t.Fatal("actual original services", err)
	}
	intent := verifyRealComposeMigrationPreparation(t, ctx, b, state, image)
	invoke := func(action string, input []byte, extra ...string) SecretMigrationStatus {
		t.Helper()
		args := []string{"host", "secret-migration", "compose", action, "--state-root", b.root, "--project", b.project}
		args = append(args, extra...)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stdin = bytes.NewReader(input)
		var output, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &diagnostic
		if err := cmd.Run(); err != nil {
			t.Fatal("public Compose migration", action, err, diagnostic.String())
		}
		var result SecretMigrationStatus
		if json.Unmarshal(output.Bytes(), &result) != nil || result.Operation == "" {
			t.Fatal("invalid public progress")
		}
		return result
	}
	plan := filepath.Join(b.root, "approved-plan.json")
	if err := writePrivateJSON(plan, intent); err != nil {
		t.Fatal(err)
	}
	pending := invoke("apply", nil, "--plan", plan, "--approve")
	if pending.Phase != lifecycle.PhaseMigrate || pending.Protection != "uninitialized" || pending.Released {
		t.Fatal("candidate was not pending manual initialization", pending)
	}
	rolledBack := invoke("rollback", nil, "--approve")
	if rolledBack.State != lifecycle.OperationRolledBack || !rolledBack.Released {
		t.Fatal("original rollback did not finish", rolledBack)
	}
	if replay := invoke("apply", nil, "--plan", plan, "--approve"); replay != rolledBack {
		t.Fatal("rollback replay created another operation")
	}
	intent, err = b.PrepareSecretMigration(ctx, intent.Plan.Mappings, intent.Plan.Resources, intent.Recipient, filepath.Join(b.root, "forward-source.age"), false)
	if err != nil {
		t.Fatal("prepare after original rollback", err)
	}
	if err := writePrivateJSON(plan, intent); err != nil {
		t.Fatal(err)
	}
	pending = invoke("apply", nil, "--plan", plan, "--approve")
	if pending.Protection != "uninitialized" {
		t.Fatal("fresh candidate protection was inherited")
	}
	// Simulate an operator-initialized candidate whose process is then lost.
	// The next public recovery must remain locked until explicit private unlock.
	passphrase := []byte("synthetic-compose-migration-private-passphrase")
	defer clear(passphrase)
	if err := b.withMigration(ctx, nil, func(ctx context.Context, _ *lifecycle.OperationJournal, _ lifecycle.OperationRecord) error {
		if _, err := b.migrationCandidateProtection(ctx, "init", passphrase); err != nil {
			return err
		}
		return b.stopMigrationCandidate(ctx)
	}); err != nil {
		t.Fatal("explicit candidate initialization/process loss", err)
	}
	locked := invoke("recover", nil)
	if locked.Released || locked.Protection != "locked" && locked.Protection != "locked_manual" || locked.Operation != pending.Operation {
		t.Fatal("lost process retained protection session", locked)
	}
	completed := invoke("recover", passphrase, "--protection", "unlock", "--stdin")
	if completed.State != lifecycle.OperationSucceeded || !completed.Released || completed.Operation != pending.Operation {
		t.Fatal("original cutover did not finish", completed)
	}
	if replay := invoke("apply", nil, "--plan", plan, "--approve"); replay != completed {
		t.Fatal("completed plan did not retain original result")
	}
	if status := invoke("status", nil); status != completed {
		t.Fatal("terminal status lost original result")
	}
	current, found, err := b.loadRuntime()
	if err != nil || !found || filepath.Base(current.ApplicationStateSource) != intent.CandidateGeneration || current.ApplicationPolicySHA256 != intent.Policy {
		t.Fatal("normal serving runtime did not select candidate", err)
	}
	if _, err := os.Lstat(intent.BackupPath); err != nil {
		t.Fatal("original encrypted backup missing", err)
	}
	ids, err := b.runner.Run(ctx, nil, "container", "ls", "--all", "--filter", "label=io.loki.migration.owner="+applicationOwnerID(b.root), "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(ids)) != "" {
		t.Fatal("completed migration left private helpers", err)
	}
}
