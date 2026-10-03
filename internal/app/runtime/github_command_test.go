package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"loki/modules/github"
	"loki/internal/process"
	mcptransport "loki/internal/transport/mcp"
)

type fakeGitHubCommandRunner struct {
	request    githubapp.CommandRequest
	checkError error
}

func (f *fakeGitHubCommandRunner) Check(context.Context) error { return f.checkError }

func (f *fakeGitHubCommandRunner) Run(_ context.Context, request githubapp.CommandRequest) (process.Result, error) {
	f.request = request
	return process.Result{ExitCode: 2, Output: "result", Truncated: true}, nil
}

func TestGitHubCommandRuntimeOperationAndStrictInput(t *testing.T) {
	runner := &fakeGitHubCommandRunner{}
	operation := GitHubCommandOperations(runner)["github_command"]
	result, err := operation.Handle(t.Context(), json.RawMessage(
		"{\"operation\":\"github_command\",\"target\":\"owner/repo\",\"args\":[\"pr\",\"list\"],\"input\":\"body\"}",
	))
	value := result.(map[string]any)
	if err != nil || runner.request.Target != "owner/repo" || runner.request.Args[0] != "pr" ||
		string(runner.request.Input) != "body" || value["exit_code"] != 2 || value["truncated"] != true {
		t.Fatal(result, runner.request, err)
	}
	for _, raw := range []string{
		"{\"operation\":\"github_command\",\"target\":\"owner/repo\",\"args\":[\"pr\"],\"token\":\"private\"}",
		"{\"operation\":\"wrong\",\"target\":\"owner/repo\",\"args\":[\"pr\"]}",
	} {
		if _, err = operation.Handle(t.Context(), json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err = GitHubCommandOperations(nil)["github_command"].Handle(t.Context(), json.RawMessage(
		"{\"operation\":\"github_command\",\"target\":\"owner/repo\",\"args\":[\"pr\"]}",
	)); err == nil {
		t.Fatal("disabled GitHub accepted")
	}
}

func TestGitHubCommandMCPBuildsNarrowRequest(t *testing.T) {
	runtime := &captureRuntime{}
	handler := mcptransport.GitHubCommandHandlers(runtime)["github"]
	result, err := handler(t.Context(), map[string]any{
		"target": "owner/repo", "command": "issue", "args": []any{"list"}, "input": "body",
	})
	if err != nil || result == nil || runtime.request["operation"] != "github_command" ||
		runtime.request["target"] != "owner/repo" || runtime.request["input"] != "body" {
		t.Fatal(result, err, runtime.request)
	}
	arguments, ok := runtime.request["args"].([]string)
	if !ok || len(arguments) != 2 || arguments[0] != "issue" || arguments[1] != "list" {
		t.Fatalf("runtime GitHub arguments = %#v", runtime.request["args"])
	}
	if _, exists := runtime.request["token"]; exists {
		t.Fatal("credential field forwarded")
	}
}

func TestGitHubCommandCheckDoesNotAcceptCommandOrCredentialInput(t *testing.T) {
	runner := &fakeGitHubCommandRunner{}
	operation := GitHubCommandOperations(runner)["github_command_check"]
	result, err := operation.Handle(t.Context(), json.RawMessage(`{"operation":"github_command_check"}`))
	if err != nil || result.(map[string]any)["ready"] != true {
		t.Fatal(result, err)
	}
	for _, raw := range []string{
		`{"operation":"github_command_check","args":["api","/user"]}`,
		`{"operation":"github_command_check","token":"private"}`,
		`{"operation":"github_command_check"} {}`,
	} {
		if _, err := operation.Handle(t.Context(), json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	runner.checkError = errors.New("CLI unavailable")
	if _, err := operation.Handle(t.Context(), json.RawMessage(`{"operation":"github_command_check"}`)); !errors.Is(err, runner.checkError) {
		t.Fatal(err)
	}
	if _, err := GitHubCommandOperations(nil)["github_command_check"].Handle(t.Context(), json.RawMessage(`{"operation":"github_command_check"}`)); err == nil {
		t.Fatal("disabled GitHub accepted")
	}
}
