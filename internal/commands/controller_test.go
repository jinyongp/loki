package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/config"
	"loki/internal/gitops"
	"loki/internal/policy"
	"loki/internal/process"
)

func fixture(t *testing.T) *Controller {
	t.Helper()
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	paths, err := policy.New(c.Root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { paths.Close() })
	manager, err := process.NewManager(process.ManagerOptions{MaxProcesses: 3, MaxOutputBytes: 10485760, Retention: time.Minute, StopGrace: time.Millisecond * 50})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return &Controller{Config: c, Paths: paths, Manager: manager, Git: &gitops.Controller{Config: c, Paths: paths}}
}
func TestFiniteAndManagedCommands(t *testing.T) {
	c := fixture(t)
	t.Setenv("LOKI_COMMAND_PRIVATE", "synthetic-secret")
	c.Config.Checks["fixture"] = config.Command{Command: []string{"/bin/sh", "-c", "printf '%s/%s' \"${LOKI_COMMAND_PRIVATE:-unset}\" \"$CUSTOM\"; exit 7"}, CWD: ".", TimeoutSeconds: 5, MaxOutputBytes: 4096, Environment: map[string]string{"CUSTOM": "public"}}
	name := "fixture"
	result, err := c.Run(t.Context(), Request{Action: "check", Name: &name})
	if err != nil || result["exit_code"] != 7 || result["output"] != "unset/public" {
		t.Fatal(result, err)
	}
	executable := "pwd"
	result, err = c.Run(t.Context(), Request{Action: "exec", Executable: &executable, CWD: ".", Timeout: 5})
	if err != nil || strings.TrimSpace(result["output"].(string)) != c.Paths.Root() {
		t.Fatal(result, err)
	}
	c.Config.Executables["fixture"] = "/usr/bin/sleep"
	result, err = c.Start(t.Context(), Request{Action: "exec", Executable: &name, Arguments: []string{"10"}, CWD: ".", Timeout: 30})
	if err != nil {
		t.Fatal(err)
	}
	id := result["session_id"].(string)
	if _, err = c.Manager.Stop(id); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Request{
		{Action: "exec", Executable: &executable, CWD: "../outside"},
		{Action: "npm", Arguments: []string{"publish"}, CWD: "."},
		{Action: "fnm", Arguments: []string{"unknown"}},
		{Action: "pnpm", CWD: "."},
	} {
		if _, err = c.Run(t.Context(), r); err == nil {
			t.Fatal(r)
		}
	}
	python := "python3"
	if _, err = c.Run(t.Context(), Request{Action: "exec", Executable: &python, Arguments: []string{"-c", "print('blocked')"}, CWD: "."}); err == nil {
		t.Fatal("inline policy bypass")
	}
}
func TestNodeSelection(t *testing.T) {
	c := fixture(t)
	for _, name := range []string{"v20.20.0", "v24.2.1", "v24.10.0", "invalid"} {
		if err := os.MkdirAll(filepath.Join(c.Paths.Root(), ".loki", "fnm", "node-versions", name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	version, err := c.NodeVersion(c.Paths.Root(), nil)
	if err != nil || version != "v24.10.0" {
		t.Fatal(version, err)
	}
	if err = os.WriteFile(filepath.Join(c.Paths.Root(), ".nvmrc"), []byte("20"), 0600); err != nil {
		t.Fatal(err)
	}
	version, err = c.NodeVersion(c.Paths.Root(), nil)
	if err != nil || version != filepath.Join(c.Paths.Root(), ".nvmrc") {
		t.Fatal(version, err)
	}
	if err = os.WriteFile(filepath.Join(c.Paths.Root(), ".node-version"), []byte("24"), 0600); err != nil {
		t.Fatal(err)
	}
	version, err = c.NodeVersion(c.Paths.Root(), nil)
	if err != nil || version != filepath.Join(c.Paths.Root(), ".node-version") {
		t.Fatal(version, err)
	}
	requested := "22"
	version, err = c.NodeVersion(c.Paths.Root(), &requested)
	if err != nil || version != requested {
		t.Fatal(version, err)
	}
	requested = "../bad"
	if _, err = c.NodeVersion(c.Paths.Root(), &requested); err == nil {
		t.Fatal("invalid version")
	}
}
