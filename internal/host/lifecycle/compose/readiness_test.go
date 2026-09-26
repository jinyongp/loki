package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"loki/internal/host/lifecycle"
)

func TestRuntimeReadinessUsesReadOnlyComposePS(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	if err := backend.Activate(t.Context(), generation, lifecycle.InstallationState{
		Scope: "user", Workspace: workspace, MCPPort: 19000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := backend.SetComponent(t.Context(), generation, "browser", true); err != nil {
		t.Fatal(err)
	}
	if err := backend.ensureAssetsAndToken(); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(backend.runtimeRoot, "assets", "compose.yaml")
	args := []string{
		"compose", "--project-name", backend.project, "--file", composePath,
		"--profile", "browser", "ps", "--status", "running", "--services",
	}
	runner.outputs[strings.Join(args, "\x00")] = []byte("runtime\nmcp\nlauncher\nexecutor\negress\nbrowser\nbrowser-proxy\n")

	readiness, err := backend.Readiness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready() || !readiness.Activated || readiness.GenerationID != generation.ID {
		t.Fatalf("runtime readiness = %#v", readiness)
	}
	want := []string{"browser", "browser-proxy", "egress", "executor", "launcher", "mcp", "runtime"}
	if strings.Join(readiness.RequiredServices, ",") != strings.Join(want, ",") ||
		strings.Join(readiness.RunningServices, ",") != strings.Join(want, ",") {
		t.Fatalf("runtime services = %#v", readiness)
	}
	calls := runner.snapshot()
	if len(calls) != 1 || !strings.HasSuffix(strings.Join(calls[0].args, " "), "ps --status running --services") {
		t.Fatalf("readiness performed mutating compose calls: %#v", calls)
	}
	if !slices.Contains(calls[0].env, "LOKI_MCP_HOST_PORT=19000") {
		t.Fatalf("readiness MCP port environment = %#v", calls[0].env)
	}
}

func TestRuntimeReadinessReportsMissingServiceWithoutMutation(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	if err := backend.Activate(t.Context(), generation, lifecycle.InstallationState{
		Scope: "user", Workspace: workspace,
	}); err != nil {
		t.Fatal(err)
	}
	if err := backend.ensureAssetsAndToken(); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(backend.runtimeRoot, "assets", "compose.yaml")
	args := []string{
		"compose", "--project-name", backend.project, "--file", composePath,
		"ps", "--status", "running", "--services",
	}
	runner.outputs[strings.Join(args, "\x00")] = []byte("runtime\nlauncher\nexecutor\negress\n")
	readiness, err := backend.Readiness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Ready() || !readiness.Activated {
		t.Fatalf("missing-service readiness = %#v", readiness)
	}
}

func TestRuntimeReadinessWithoutActivationIsBlockedStateNotError(t *testing.T) {
	backend, runner, _ := composeBackendFixture(t)
	readiness, err := backend.Readiness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Activated || readiness.Ready() || len(runner.snapshot()) != 0 {
		t.Fatalf("inactive readiness = %#v calls=%#v", readiness, runner.snapshot())
	}
}

func TestOpenBackendDoesNotCreateDiagnosticRuntimeState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lifecycle")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := Open(Config{StateRoot: root, Runner: &fakeRunner{}})
	if !os.IsNotExist(err) {
		t.Fatalf("read-only backend open error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "runtime")); !os.IsNotExist(statErr) {
		t.Fatalf("read-only backend open created runtime state: %v", statErr)
	}
}
