package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"loki/modules/github"
)

type adminCall func(context.Context, any) (json.RawMessage, error)

func (f adminCall) Call(ctx context.Context, request any) (json.RawMessage, error) {
	return f(ctx, request)
}

func TestGitHubAppKeyCLIUsesTerminalOnlyAndRedacts(t *testing.T) {
	private := "synthetic-private-key-input"
	calls := 0
	client := adminCall(func(_ context.Context, request any) (json.RawMessage, error) {
		calls++
		values := request.(map[string]any)
		if values["operation"] != "github_app_key_set" || values["value"] != private {
			t.Fatalf("request: %#v", values)
		}
		return json.RawMessage(`{"configured":true,"rotated":false}`), nil
	})
	read := func(context.Context, io.Writer, bool) (string, error) { return private, nil }
	var stdout, stderr bytes.Buffer
	if code := executeAdministrationInput([]string{"github", "app-key", "set"}, client, read, &stdout, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if calls != 1 || strings.Contains(stdout.String()+stderr.String(), private) {
		t.Fatal("private key disclosed")
	}
	for _, args := range [][]string{{"github", "app-key", "set", private}, {"github", "app-key", "set", "--file", "key.pem"}} {
		if code := executeAdministrationInput(args, client, read, &stdout, &stderr); code != 2 {
			t.Fatalf("accepted key argument: %#v", args)
		}
	}
}

func TestGitHubIssueFieldsCLIUsesTypedNarrowRequest(t *testing.T) {
	client := adminCall(func(_ context.Context, request any) (json.RawMessage, error) {
		values := request.(map[string]any)
		if values["operation"] != "github_values_set" || values["target"] != "owner/repo" || values["issue"] != 7 {
			t.Fatalf("request: %#v", values)
		}
		fields, ok := values["values"].([]githubapp.Value)
		if !ok || len(fields) != 1 || fields[0].FieldID != 3 || fields[0].Text != "High" {
			t.Fatalf("values: %#v", values["values"])
		}
		return json.RawMessage(`{"values":[]}`), nil
	})
	read := func(context.Context, io.Writer, bool) (string, error) {
		t.Fatal("credential reader invoked")
		return "", nil
	}
	var stdout, stderr bytes.Buffer
	args := []string{"github", "values", "set", "owner/repo", `[{"field_id":3,"data_type":"single_select","text":"High"}]`, "--issue", "7"}
	if code := executeAdministrationInput(args, client, read, &stdout, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	for _, option := range []string{"--token", "--url", "--headers", "--method"} {
		if code := executeAdministrationInput([]string{"github", "fields", "list", "owner/repo", option, "private"}, client, read, &stdout, &stderr); code != 2 {
			t.Fatal("accepted", option)
		}
	}
}
