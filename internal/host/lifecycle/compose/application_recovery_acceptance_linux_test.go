//go:build linux

package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"filippo.io/age"
	"loki/internal/host/lifecycle"
)

func verifyRealComposeNewDeployment(t *testing.T, ctx context.Context, source *Backend, binary string, backup lifecycle.BackupRecord, identity *age.X25519Identity) {
	t.Helper()
	current, found, err := source.loadRuntime()
	if err != nil || !found {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	b, err := New(Config{StateRoot: filepath.Join(t.TempDir(), "new-owner"), Project: source.project + "-new", Runner: ExecRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	current.MCPPort = port
	current.ApplicationRuntimeRoot, current.ApplicationStateSource, current.ApplicationPolicySHA256 = "", "", ""
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		if latest, exists, _ := b.loadRuntime(); exists {
			current = latest
		}
		if _, err := b.compose(cleanup, current, "down", "--volumes", "--remove-orphans"); err != nil {
			t.Error(err)
		}
		for _, name := range []string{"application-state", "runtime-state"} {
			if found, _ := b.volumeExists(cleanup, b.volumeName(name)); found {
				if _, err := b.runner.Run(cleanup, nil, "volume", "rm", b.volumeName(name)); err != nil {
					t.Error(err)
				}
			}
		}
		if current.ApplicationRuntimeRoot != "" {
			_, err := b.runner.(PrivateRunner).RunPrivate(cleanup, nil, nil, "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--user", "0:0", "--log-driver", "none", "--mount", applicationBind(current.ApplicationRuntimeRoot, "/owned", false), "--entrypoint", "/bin/sh", current.CoreImage, "-ec", "find /owned -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +")
			if err != nil {
				t.Error(err)
			}
			if err := os.Remove(current.ApplicationRuntimeRoot); err != nil {
				t.Error(err)
			}
			if err := os.Remove(filepath.Dir(current.ApplicationRuntimeRoot)); err != nil {
				t.Error(err)
			}
		}
	})
	store, err := lifecycle.EnsureFileStore(b.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeInstall(ctx, *backup.Installed, lifecycle.InstallationState{Scope: "system", Workspace: current.Workspace, MCPPort: port}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitGeneration(ctx, *backup.Installed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := b.ensureAssetsAndToken(); err != nil {
		t.Fatal(err)
	}
	oldToken, err := os.ReadFile(source.tokenPath())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(oldToken)
	newToken, err := os.ReadFile(b.tokenPath())
	if err != nil || bytes.Equal(oldToken, newToken) {
		t.Fatal("new owner inherited historical MCP authentication", err)
	}
	clear(newToken)
	if err := b.prepareApplicationValues(ctx, &current); err != nil {
		t.Fatal(err)
	}
	if err := b.saveRuntime(current); err != nil {
		t.Fatal(err)
	}
	if _, err := b.compose(ctx, current, "up", "--detach", "--wait", "--wait-timeout", "40", "application-values", "launcher"); err != nil {
		t.Fatal(err)
	}
	invoke := func(action string, input []byte, target any, extra ...string) {
		t.Helper()
		args := append([]string{"host", "application-restore", "compose", action, "--state-root", b.root, "--project", b.project}, extra...)
		command := exec.CommandContext(ctx, binary, args...)
		command.Stdin = bytes.NewReader(input)
		var output, diagnostic bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostic
		if err := command.Run(); err != nil {
			t.Fatal("public new-deployment recovery", action, err, diagnostic.String())
		}
		if json.Unmarshal(output.Bytes(), target) != nil {
			t.Fatal("invalid recovery response")
		}
	}
	var plan lifecycle.ApplicationRestoreIntent
	invoke("prepare-new-deployment", nil, &plan, "--source-state-root", source.root, "--backup-id", backup.ID)
	if !plan.Valid() || !plan.NewDeployment || plan.Before.DeploymentID == backup.ApplicationBackup.Intent.Identity.DeploymentID || plan.Before.RunnerID == backup.ApplicationBackup.Intent.Identity.RunnerID {
		t.Fatal("recovery did not isolate the historical deployment")
	}
	planPath := filepath.Join(b.root, "new-deployment-plan.json")
	if err := writePrivateJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	var pending, completed ApplicationRestoreStatus
	invoke("apply", nil, &pending, "--plan", planPath, "--approve")
	if pending.Protection != "uninitialized" || pending.Released {
		t.Fatal("new deployment initialized protection implicitly", pending)
	}
	key := filepath.Join(b.root, "operator-identity")
	if err := os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(key)
	pass := []byte("synthetic-fresh-deployment-protection")
	defer clear(pass)
	invoke("recover", pass, &completed, "--identity-file", key, "--protection", "init", "--stdin")
	if completed.State != lifecycle.OperationSucceeded || !completed.Released || !completed.Sealed || completed.Operation != pending.Operation {
		t.Fatal("new deployment did not complete app-only recovery", completed)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+string(oldToken))
	client := http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("historical authentication was admitted by the fresh deployment", response.StatusCode)
	}
	if records, err := lifecycle.ReadOperationSnapshot(b.root); err != nil || len(records) != 1 || records[0].ApplicationRestore == nil {
		t.Fatal("new owner imported historical host operation authority", err)
	}
}
