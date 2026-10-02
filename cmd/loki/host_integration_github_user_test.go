package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

type githubUserAdminCaller func(context.Context, any) (json.RawMessage, error)

func (f githubUserAdminCaller) Call(ctx context.Context, request any) (json.RawMessage, error) {
	return f(ctx, request)
}

func TestGitHubUserCommandsUseSetupAndRejectAccountSelectors(t *testing.T) {
	for _, action := range []string{"login", "logout", "user-status"} {
		var stdout, stderr bytes.Buffer
		if code := runHostGitHubUser(action, []string{"--account", "example-user", "github"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "flag provided but not defined") {
			t.Fatal("user command accepted account selector", action, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runHostGitHubUser("login", []string{"--system", "github"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "setup --system github") {
		t.Fatal("login did not direct users to setup", code, stderr.String())
	}
}

func TestGitHubUserAdminDoesNotForwardSocketOrReadSecrets(t *testing.T) {
	client := githubUserAdminCaller(func(_ context.Context, input any) (json.RawMessage, error) {
		request := input.(map[string]any)
		if request["operation"] != "github_user_status" || request["account"] != "example-user" || len(request) != 2 {
			t.Fatal("invalid runtime user request", request)
		}
		return json.RawMessage(`{"status":"unconfigured","account":"example-user"}`), nil
	})
	read := func(context.Context, io.Writer, bool) (string, error) {
		t.Fatal("user authorization read a secret input")
		return "", nil
	}
	var stdout, stderr bytes.Buffer
	if code := executeAdministrationInput([]string{"github", "user", "status", "--runtime-socket", "/run/loki/runtime/control.sock", "example-user"}, client, read, &stdout, &stderr); code != 0 || !json.Valid(stdout.Bytes()) {
		t.Fatal("user administration failed", code, stderr.String())
	}
}

func TestGitHubRefreshAdminForwardsOnlyClosedOperation(t *testing.T) {
	client := githubUserAdminCaller(func(_ context.Context, input any) (json.RawMessage, error) {
		request := input.(map[string]any)
		if request["operation"] != "github_refresh" || len(request) != 1 {
			t.Fatal("invalid refresh request", request)
		}
		return json.RawMessage(`{"refreshed":true}`), nil
	})
	read := func(context.Context, io.Writer, bool) (string, error) {
		t.Fatal("refresh attempted to read a credential")
		return "", nil
	}
	var stdout, stderr bytes.Buffer
	if code := executeAdministrationInput([]string{"github", "refresh", "--runtime-socket", "/run/loki/runtime/control.sock"}, client, read, &stdout, &stderr); code != 0 || !json.Valid(stdout.Bytes()) {
		t.Fatal("refresh administration failed", code, stderr.String())
	}
}

func TestGitHubUserRelayRejectsCredentialAndTrailingInput(t *testing.T) {
	for _, input := range []string{`{"action":"begin","account":"example-user","access_token":"private"}`, `{"action":"poll","device_code":"private"}`, `{} {}`, strings.Repeat("x", 4097)} {
		if _, err := readGitHubUserRequest(strings.NewReader(input)); err == nil {
			t.Fatal("invalid user authorization relay accepted")
		}
	}
	request, err := readGitHubUserRequest(strings.NewReader(`{"action":"begin","account":"example-user"}`))
	if err != nil || request.Action != "begin" || request.Account != "example-user" {
		t.Fatal("valid user relay failed", err)
	}
}
