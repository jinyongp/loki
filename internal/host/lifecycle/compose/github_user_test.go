package compose

import (
	"slices"
	"strings"
	"testing"

	"loki/internal/integrations/github/setup"
	"loki/internal/host/lifecycle"
)

func TestGitHubUserRelayUsesRuntimeSocketAndClosedCommand(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	if err := backend.Activate(t.Context(), composeGeneration(t, "1.2.3", "b"), lifecycle.InstallationState{Scope: "user", Workspace: workspace, MCPPort: 19000}); err != nil {
		t.Fatal(err)
	}
	before := len(runner.snapshot())
	_, err := backend.GitHubUserAuthorization(t.Context(), githubsetup.UserRequest{Action: "status"})
	if err == nil {
		t.Fatal("empty runtime output accepted")
	}
	calls := runner.snapshot()
	args := calls[len(calls)-1].args
	wantSuffix := []string{"exec", "-T", "runtime", "/opt/loki/bin/loki", "github", "user", "status", "--runtime-socket", "/run/loki/runtime/control.sock", "--", ""}
	if len(calls) != before+1 || len(args) < len(wantSuffix) || !slices.Equal(args[len(args)-len(wantSuffix):], wantSuffix) {
		t.Fatal("invalid user relay argv", args)
	}
	runner.outputs[strings.Join(args, "\x00")] = []byte(`{"status":"unconfigured","accounts":[{"account":"example-user","status":"unconfigured"}]}`)
	if view, err := backend.GitHubUserAuthorization(t.Context(), githubsetup.UserRequest{Action: "status"}); err != nil || view.Status != "unconfigured" {
		t.Fatal("valid runtime output rejected", view, err)
	}
	before = len(runner.snapshot())
	for _, request := range []githubsetup.UserRequest{{Action: "api", Account: "example-user"}, {Action: "begin", Account: "--token"}, {Action: "poll", Account: "example-user", SessionID: strings.Repeat("a", 64)}, {Action: "logout", Account: "example-user", SessionID: "secret"}} {
		if _, err := backend.GitHubUserAuthorization(t.Context(), request); err == nil {
			t.Fatal("unsafe relay accepted", request)
		}
	}
	if len(runner.snapshot()) != before {
		t.Fatal("unsafe relay reached runtime")
	}
}
