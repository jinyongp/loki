package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

type secretProcessClient struct {
	request any
	calls   int
}

func (c *secretProcessClient) Call(_ context.Context, request any) (json.RawMessage, error) {
	c.request = request
	c.calls++
	return json.RawMessage(`{"started":true}`), nil
}

func TestSecretProcessBuildsBrokerRequest(t *testing.T) {
	client := &secretProcessClient{}
	var stdout, stderr bytes.Buffer
	code := executeSecretProcess([]string{
		"start", "--profile", "project", "--secret", "TOKEN", "--secret", "API_KEY",
		"--request-id", "00000000-0000-0000-0000-000000000000", "--dir", "/workspace/app", "web",
	}, client, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || client.calls != 1 {
		t.Fatalf("code=%d stderr=%q calls=%d", code, stderr.String(), client.calls)
	}
	request := client.request.(map[string]any)
	if request["operation"] != "devtools_call" || request["command"] != "process start" || request["profile"] != "project" {
		t.Fatalf("request = %#v", request)
	}
	secrets := request["secrets"].([]string)
	if len(secrets) != 2 || secrets[0] != "TOKEN" || secrets[1] != "API_KEY" {
		t.Fatalf("secrets = %#v", secrets)
	}
	var input map[string]any
	if json.Unmarshal(request["input"].(json.RawMessage), &input) != nil || input["dir"] != "/workspace/app" || input["args"].([]any)[0] != "web" {
		t.Fatalf("input = %#v", input)
	}
	if stdout.String() != "{\n  \"started\": true\n}\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestSecretProcessRejectsIncompleteInput(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"stop"},
		{"start", "--profile", "project", "--request-id", "id", "web"},
		{"start", "--profile", "project", "--secret", "TOKEN", "web"},
		{"restart", "--profile", "project", "--secret", "TOKEN", "--request-id", "id", "--dir", "/tmp", "execution"},
	} {
		client := &secretProcessClient{}
		if code := executeSecretProcess(arguments, client, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 || client.calls != 0 {
			t.Fatalf("arguments=%#v code=%d calls=%d", arguments, code, client.calls)
		}
	}
}
