package service

import (
	"context"
	"encoding/json"
	"testing"

	githubapp "loki/internal/integrations/github"
)

type fakeGitHubProvider struct {
	reads    []githubapp.ProviderReadRequest
	comments []githubapp.CommentRequest
}

func (f *fakeGitHubProvider) Read(_ context.Context, request githubapp.ProviderReadRequest) (githubapp.ProviderReadResult, error) {
	f.reads = append(f.reads, request)
	return githubapp.ProviderReadResult{
		Target: request.Target, Action: request.Action,
		Repository: map[string]any{"full_name": request.Target, "default_branch": "main", "html_url": "https://github.com/" + request.Target},
	}, nil
}

func (f *fakeGitHubProvider) Comment(_ context.Context, request githubapp.CommentRequest) (githubapp.CommentResult, error) {
	f.comments = append(f.comments, request)
	return githubapp.CommentResult{
		Target: request.Target, Number: request.Number, CommentID: 77,
		HTMLURL:   "https://github.com/owner/repo/issues/7#issuecomment-77",
		RequestID: request.RequestID, OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, nil
}

func TestGitHubProviderRuntimeOperationsAreTypedAndStrict(t *testing.T) {
	fake := &fakeGitHubProvider{}
	ops := GitHubProviderOperations(fake)
	readRaw := json.RawMessage(`{"operation":"github_provider_read","action":"repository","target":"owner/repo"}`)
	result, err := ops["github_provider_read"].Handle(t.Context(), readRaw)
	if err != nil {
		t.Fatal(err)
	}
	value := result.(map[string]any)
	if value["target"] != "owner/repo" || value["action"] != "repository" || len(fake.reads) != 1 {
		t.Fatalf("typed read result = %#v calls=%#v", value, fake.reads)
	}

	writeRaw := json.RawMessage(`{"operation":"github_provider_comment","action":"comment","target":"owner/repo","number":7,"body":"hello","request_id":"123e4567-e89b-42d3-a456-426614174000"}`)
	result, err = ops["github_provider_comment"].Handle(t.Context(), writeRaw)
	if err != nil {
		t.Fatal(err)
	}
	value = result.(map[string]any)
	if value["comment_id"] != int64(77) || value["operation_id"] == "" || len(fake.comments) != 1 {
		t.Fatalf("typed write result = %#v calls=%#v", value, fake.comments)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"operation":"github_provider_read","action":"repository","target":"owner/repo","command":"issue"}`),
		json.RawMessage(`{"operation":"github_provider_comment","action":"comment","target":"owner/repo","number":7,"body":"x","request_id":"123e4567-e89b-42d3-a456-426614174000","args":["--repo","other/repo"]}`),
	} {
		name := "github_provider_read"
		if string(raw)[14] == 'g' {
			name = "github_provider_comment"
		}
		if _, err := ops[name].Handle(t.Context(), raw); err == nil {
			t.Fatalf("provider RPC accepted hidden command authority: %s", raw)
		}
	}
}

func TestGitHubProviderMCPBuildsNarrowReadAndWriteRequests(t *testing.T) {
	runtime := &captureRuntime{}
	handlers := GitHubProviderHandlers(runtime)

	result, err := handlers["github_read"](t.Context(), map[string]any{
		"action": "pull_request", "target": "owner/repo", "number": 8,
	})
	if err != nil || result == nil || runtime.request["operation"] != "github_provider_read" ||
		runtime.request["action"] != "pull_request" || runtime.request["number"] != int64(8) {
		t.Fatalf("github_read request = %#v err=%v", runtime.request, err)
	}

	result, err = handlers["github_write"](t.Context(), map[string]any{
		"action": "comment", "target": "owner/repo", "number": 9, "body": "hello",
		"request_id": "123e4567-e89b-42d3-a456-426614174001",
	})
	if err != nil || result == nil || runtime.request["operation"] != "github_provider_comment" ||
		runtime.request["body"] != "hello" || runtime.request["request_id"] == "" {
		t.Fatalf("github_write request = %#v err=%v", runtime.request, err)
	}

	if _, err = handlers["github_read"](t.Context(), map[string]any{"action": "comment", "target": "owner/repo"}); err == nil {
		t.Fatal("github_read accepted mutation action")
	}
	if _, err = handlers["github_write"](t.Context(), map[string]any{"action": "repository", "target": "owner/repo"}); err == nil {
		t.Fatal("github_write accepted read action")
	}
}
