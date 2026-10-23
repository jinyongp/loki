package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/app/executor"
	"loki/internal/daemon"
	hostpolicy "loki/internal/host/policy"
	"loki/modules/execution/jobs"
)

func validExecutorLayout(t *testing.T) executorLayout {
	t.Helper()
	return executorLayout{
		Socket:            filepath.Join(t.TempDir(), "executor.sock"),
		SocketGID:         os.Getgid(),
		AgentUID:          1001,
		ExecutorUID:       1002,
		LauncherSocket:    filepath.Join(t.TempDir(), "launcher.sock"),
		LauncherUID:       0,
		PolicySHA256:      strings.Repeat("a", 64),
		RunTimeoutSeconds: 30,
	}
}

func TestBuildExecutorConstructsNarrowRoleInputs(t *testing.T) {
	layout := validExecutorLayout(t)
	options, err := buildExecutor(layout)
	if err != nil {
		t.Fatal(err)
	}
	if options.Socket != layout.Socket || options.SocketGID != layout.SocketGID ||
		options.AgentUID != layout.AgentUID || options.RunTimeout != 30*time.Second {
		t.Fatalf("executor options = %#v", options)
	}
	if _, ok := options.Runner.(*jobs.Service); !ok {
		t.Fatalf("runner type = %T", options.Runner)
	}
	if options.Ready != nil {
		t.Fatal("builder installed runtime readiness callback")
	}
	var _ executor.Runner = options.Runner
}

func TestBuildExecutorRejectsUnsafeLayout(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*executorLayout)
	}{
		{"socket-relative", func(l *executorLayout) { l.Socket = "executor.sock" }},
		{"socket-root", func(l *executorLayout) { l.Socket = "/" }},
		{"socket-gid", func(l *executorLayout) { l.SocketGID = -1 }},
		{"launcher-relative", func(l *executorLayout) { l.LauncherSocket = "launcher.sock" }},
		{"launcher-root-path", func(l *executorLayout) { l.LauncherSocket = "/" }},
		{"agent-root", func(l *executorLayout) { l.AgentUID = 0 }},
		{"executor-root", func(l *executorLayout) { l.ExecutorUID = 0 }},
		{"shared-identity", func(l *executorLayout) { l.ExecutorUID = l.AgentUID }},
		{"launcher-non-root", func(l *executorLayout) { l.LauncherUID = 1003 }},
		{"policy-digest", func(l *executorLayout) { l.PolicySHA256 = "bad" }},
		{"timeout-zero", func(l *executorLayout) { l.RunTimeoutSeconds = 0 }},
		{"timeout-high", func(l *executorLayout) { l.RunTimeoutSeconds = maxExecutorRunTimeoutSeconds + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout := validExecutorLayout(t)
			test.mutate(&layout)
			if _, err := buildExecutor(layout); err == nil {
				t.Fatal("unsafe executor layout was accepted")
			}
		})
	}
}

func TestExecutorLayoutStrictlyRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "executor.json")
	if err := os.WriteFile(path, []byte(`{"Unknown":"value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var layout executorLayout
	if err := daemon.ReadJSON(path, &layout); err == nil {
		t.Fatal("unknown executor layout field was accepted")
	}
}

func TestRequireExecutorIdentity(t *testing.T) {
	if err := requireExecutorIdentity(1002, 1002); err != nil {
		t.Fatal(err)
	}
	if err := requireExecutorIdentity(0, 1002); err == nil {
		t.Fatal("root executor was accepted")
	}
	if err := requireExecutorIdentity(1001, 1002); err == nil {
		t.Fatal("wrong executor UID was accepted")
	}
}

func TestExecutorBindsSameApplicationPolicyAsLauncher(t *testing.T) {
	root := t.TempDir()
	configuration := filepath.Join(root, "loki.toml")
	if err := os.WriteFile(configuration, []byte("root = \"/workspace\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(root, "applications.json")
	body := []byte(`{"resources":[{"id":"9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2","alias":"fixture","repository":"repo:fixture","principals":[{"principal":"agent","execute":true}],"environments":["common"],"keys":{"TOKEN":"secret"},"network":"none","destinations":[]}]}`)
	if err := os.WriteFile(catalog, body, 0600); err != nil {
		t.Fatal(err)
	}
	contract, err := filepath.Abs("../../packaging/images/config/execution-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	expected, _, _, err := hostpolicy.CompileFilesWithApplications(configuration, "", contract, catalog)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _, err := hostpolicy.CompileFiles(configuration, "", contract)
	if err != nil || legacy.Digest() == expected.Digest() {
		t.Fatal("fixture does not distinguish missing application policy", err)
	}
	layout := executorLayout{ApplicationCatalog: catalog}
	if err := resolveExecutorPolicy(&layout, configuration, "", contract); err != nil || layout.PolicySHA256 != expected.Digest() {
		t.Fatal("executor did not bind the selected application grants", err)
	}
	if err := os.Remove(catalog); err != nil {
		t.Fatal(err)
	}
	if err := resolveExecutorPolicy(&layout, configuration, "", contract); err == nil {
		t.Fatal("missing selected application policy silently fell back")
	}
}
