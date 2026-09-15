package packaging

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type composeLife struct {
	env, state, workspace, log string
}

func newComposeLife(t *testing.T) composeLife {
	t.Helper()
	root := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("testdata", "fake-docker"))
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "docker")
	writeExecutable(t, fake, string(fixture))
	compose := filepath.Join(root, "compose.yaml")
	if err = os.WriteFile(compose, []byte("services: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state, workspace, log := filepath.Join(root, "state"), filepath.Join(root, "workspace"), filepath.Join(root, "docker.log")
	env := strings.Join([]string{
		"LOKI_COMPOSE_STATE_DIR=" + state,
		"LOKI_COMPOSE_FILE=" + compose,
		"LOKI_DOCKER=" + fake,
		"LOKI_HEALTH_ATTEMPTS=1",
		"LOKI_HEALTH_INTERVAL=0",
		"FAKE_DOCKER_LOG=" + log,
	}, "\x00")
	return composeLife{env: env, state: state, workspace: workspace, log: log}
}

func (f composeLife) run(t *testing.T, input string, want bool, args ...string) string {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "loki-compose-lifecycle.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, args...)
	cmd.Env = append(os.Environ(), strings.Split(f.env, "\x00")...)
	cmd.Stdin = strings.NewReader(input)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err = cmd.Run()
	if want && err != nil {
		t.Fatalf("%v failed: %v\n%s", args, err, &output)
	}
	if !want && err == nil {
		t.Fatalf("%v unexpectedly succeeded\n%s", args, &output)
	}
	return output.String()
}

func (f composeLife) init(t *testing.T) {
	t.Helper()
	f.run(t, "initial-secret-0123456789-abcdefghijklmnopqrstuv", true, "initialize", f.workspace, "loki:v1")
}

func TestComposeLifecycleInitializesWithoutTokenLeak(t *testing.T) {
	f := newComposeLife(t)
	output := f.run(t, "initial-secret-0123456789-abcdefghijklmnopqrstuv", true, "initialize", f.workspace, "loki:v1")
	log, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output+string(log), "initial-secret-0123456789-abcdefghijklmnopqrstuv") {
		t.Fatal("token leaked to output or Docker arguments")
	}
	token, err := os.ReadFile(filepath.Join(f.state, "mcp-token"))
	if err != nil || string(token) != "initial-secret-0123456789-abcdefghijklmnopqrstuv" {
		t.Fatalf("token = %q, %v", token, err)
	}
	info, err := os.Stat(filepath.Join(f.state, "mcp-token"))
	if err != nil || info.Mode().Perm() != 0444 {
		t.Fatalf("token mode = %v, %v", info.Mode().Perm(), err)
	}
	info, err = os.Stat(f.state)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("state mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestComposeLifecycleBackupRestoreUpgradeRollback(t *testing.T) {
	f := newComposeLife(t)
	f.init(t)
	backup := filepath.Join(filepath.Dir(f.state), "backup")
	f.run(t, "", true, "backup", backup)
	for _, name := range []string{"manifest", "runtime-state.tar", "runner-state.tar"} {
		if _, err := os.Stat(filepath.Join(backup, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	f.run(t, "", true, "restore", backup)
	f.run(t, "", true, "upgrade", "loki:v2")
	if current, _ := os.ReadFile(filepath.Join(f.state, "current-image")); string(current) != "loki:v2\n" {
		t.Fatalf("upgraded image = %q", current)
	}
	f.run(t, "", true, "rollback")
	if current, _ := os.ReadFile(filepath.Join(f.state, "current-image")); string(current) != "loki:v1\n" {
		t.Fatalf("rolled back image = %q", current)
	}
}

func TestComposeLifecycleRecoversFailuresAndRejectsBadInput(t *testing.T) {
	f := newComposeLife(t)
	f.run(t, "secret", false, "initialize", "relative", "loki:v1")
	f.run(t, "secret", false, "initialize", f.workspace, "not-loki")
	f.init(t)
	f.run(t, "", false, "upgrade", "loki:bad-health")
	if current, _ := os.ReadFile(filepath.Join(f.state, "current-image")); string(current) != "loki:v1\n" {
		t.Fatalf("failed upgrade image = %q", current)
	}
	f.run(t, "next-secret-0123456789-abcdefghijklmnopqrstuvwxyz", true, "rotate-credentials")
	f.run(t, "bad-secret\n", false, "rotate-credentials")
	if token, _ := os.ReadFile(filepath.Join(f.state, "mcp-token")); string(token) != "next-secret-0123456789-abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("invalid rotation changed token = %q", token)
	}
	corrupt := filepath.Join(filepath.Dir(f.state), "corrupt")
	if err := os.Mkdir(corrupt, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, "manifest"), []byte("format=1\nruntime-state=no\nrunner-state=no\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t, "", false, "restore", corrupt)
}
