package compose

import (
	"slices"
	"strings"
	"testing"

	"loki/internal/host/lifecycle"
)

func TestGitHubRefreshUsesClosedRuntimeCommandWithoutRestart(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	if err := backend.Activate(t.Context(), composeGeneration(t, "1.2.3", "b"), lifecycle.InstallationState{Scope: "user", Workspace: workspace, MCPPort: 19000}); err != nil {
		t.Fatal(err)
	}
	before := len(runner.snapshot())
	if err := backend.GitHubRefresh(t.Context()); err == nil {
		t.Fatal("refresh accepted empty runtime output")
	}
	calls := runner.snapshot()
	args := calls[len(calls)-1].args
	want := []string{"exec", "-T", "runtime", "/opt/loki/bin/loki", "github", "refresh", "--runtime-socket", "/run/loki/runtime/control.sock"}
	if len(calls) != before+1 || len(args) < len(want) || !slices.Equal(args[len(args)-len(want):], want) {
		t.Fatal("refresh did not use the closed runtime operation", args)
	}
	runner.outputs[strings.Join(args, "\x00")] = []byte(`{"refreshed":true}`)
	if err := backend.GitHubRefresh(t.Context()); err != nil {
		t.Fatal("valid refresh failed", err)
	}
	for _, raw := range []string{`{"refreshed":false}`, `{"token":"private"}`, `{"refreshed":true} {}`} {
		runner.outputs[strings.Join(args, "\x00")] = []byte(raw)
		if err := backend.GitHubRefresh(t.Context()); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe refresh response accepted or exposed", err)
		}
	}
}
