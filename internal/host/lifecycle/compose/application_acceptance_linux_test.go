//go:build linux

package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealComposeApplicationService(t *testing.T) {
	image := os.Getenv("LOKI_TEST_NATIVE_SECRET_IMAGE")
	if image == "" {
		if os.Getenv("LOKI_REQUIRE_NATIVE_SECRET_COMPOSE") == "1" {
			t.Fatal("native shipping image is required")
		}
		t.Skip("native packaging suite supplies the shipping image")
	}
	if !strings.Contains(image, "@sha256:") {
		t.Fatal("an immutable fixture image is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "operator")
	sum := sha256.Sum256([]byte(root))
	project := "loki-secret-" + hex.EncodeToString(sum[:6])
	runner := ExecRunner{}
	b, err := New(Config{StateRoot: stateRoot, Project: project, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	state := runtimeState{Version: 1, CoreImage: image, Workspace: workspace}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if _, err := b.compose(cleanup, state, "down", "--volumes", "--remove-orphans"); err != nil {
			t.Error("fixture Compose cleanup failed", err)
		}
		if _, err := runner.Run(cleanup, nil, "volume", "rm", b.volumeName("application-state")); err != nil {
			t.Error("fixture app volume cleanup failed", err)
		}
		if state.ApplicationRuntimeRoot != "" {
			if _, err := runner.Run(cleanup, nil, "run", "--rm", "--network", "none", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--user", "0:0", "--mount", applicationBind(state.ApplicationRuntimeRoot, "/fixture", false), "--entrypoint", "/bin/sh", image, "-ec", "find /fixture -mindepth 1 -maxdepth 1 -exec rm -rf {} +"); err != nil {
				t.Error("fixture tmpfs cleanup failed", err)
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
		t.Fatal(err)
	}
	if _, err := b.compose(ctx, state, "up", "--detach", "--wait", "--wait-timeout", "45", "application-values"); err != nil {
		t.Fatal(err)
	}
	idRaw, err := b.compose(ctx, state, "ps", "--quiet", "application-values")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(idRaw))
	raw, err := runner.Run(ctx, nil, "inspect", id)
	if err != nil {
		t.Fatal(err)
	}
	var containers []struct {
		Config     struct{ User string }
		HostConfig struct {
			NetworkMode    string
			CapAdd         []string
			ReadonlyRootfs bool
		}
		Mounts []struct{ Source, Destination string }
	}
	if json.Unmarshal(raw, &containers) != nil || len(containers) != 1 {
		t.Fatal("invalid container inspection")
	}
	c := containers[0]
	if c.Config.User != "10006:10006" || c.HostConfig.NetworkMode != "none" || len(c.HostConfig.CapAdd) != 0 || !c.HostConfig.ReadonlyRootfs {
		t.Fatal("broker identity or isolation changed")
	}
	for _, mount := range c.Mounts {
		if mount.Destination == "/workspace" || strings.Contains(mount.Destination, "docker.sock") || mount.Destination == "/var/lib/loki/runtime" || mount.Destination == "/var/lib/loki/app-store" {
			t.Fatal("broker acquired foreign authority")
		}
	}
	admin := func(action string, input []byte) map[string]any {
		t.Helper()
		layout := filepath.Join(filepath.Dir(filepath.Dir(state.ApplicationStateSource)), "secrets-admin.json")
		args := []string{"run", "--rm", "--interactive", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--user", "0:0", "--mount", applicationBind(filepath.Join(state.ApplicationRuntimeRoot, "admin"), "/run/loki/secrets/admin", false), "--mount", applicationBind(layout, "/fixture/admin.json", true), "--entrypoint", "/opt/loki/bin/loki", image, "secret", "protection", action, "--layout", "/fixture/admin.json"}
		if input != nil {
			args = append(args, "--stdin")
		}
		raw, err := runner.RunPrivate(ctx, nil, bytes.NewReader(input), args...)
		if err != nil {
			t.Fatal("private protection helper failed", action)
		}
		var result map[string]any
		if json.Unmarshal(raw, &result) != nil {
			t.Fatal("invalid protection metadata")
		}
		return result
	}
	status := admin("status", nil)
	if status["state"] != "uninitialized" {
		t.Fatal("installation initialized protection implicitly")
	}
	passphrase := []byte("synthetic-owned-compose-fixture-passphrase")
	defer clear(passphrase)
	admin("init", passphrase)
	admin("unlock", passphrase)
	if admin("status", nil)["state"] != "unlocked" {
		t.Fatal("manual unlock did not open the synthetic vault")
	}
	if _, err := b.compose(ctx, state, "restart", "application-values"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.compose(ctx, state, "up", "--detach", "--wait", "--wait-timeout", "30", "application-values"); err != nil {
		t.Fatal(err)
	}
	status = admin("status", nil)
	if status["state"] != "locked" {
		t.Fatal("restart retained an unlocked session")
	}
}
