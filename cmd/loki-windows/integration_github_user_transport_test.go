package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"loki/internal/host/githubsetup"
	windowshost "loki/internal/host/windows"
)

type githubUserRelayRunner struct {
	integrationProgressRunner
	response string
	input    []byte
}

func (r *githubUserRelayRunner) RunInput(_ context.Context, _ string, args []string, input []byte) (windowshost.NativeProbe, error) {
	if !strings.Contains(strings.Join(args, " "), "host integration login --system --browser-request github") || strings.Contains(strings.Join(args, " "), "session-marker") {
		r.t.Fatal("invalid device relay argv", args)
	}
	r.input = append([]byte(nil), input...)
	return windowshost.NativeProbe{Stdout: r.response}, nil
}

func TestWindowsGitHubUserRelayPreservesPublicRequestsAndRejectsBadViews(t *testing.T) {
	var output bytes.Buffer
	runner := &githubUserRelayRunner{integrationProgressRunner: integrationProgressRunner{t: t, output: &output}, response: `{"status":"ready","account":"example-user"}`}
	transport := windowsGitHubUserTransport(windowshost.OperatorClient{WSL: windowshost.WSLClient{Runner: runner}}, "loki-mcp")
	request := githubsetup.UserRequest{Action: "poll", SessionID: "session-marker"}
	if view, err := transport(t.Context(), request); err != nil || view.Status != "ready" {
		t.Fatal("device relay failed", view, err)
	}
	var actual githubsetup.UserRequest
	if err := json.Unmarshal(runner.input, &actual); err != nil || actual != request {
		t.Fatal("device relay changed request", err)
	}
	for _, response := range []string{`{}`, `{"status":"ready"}`, `not-json`} {
		runner.response = response
		if _, err := transport(t.Context(), request); err == nil {
			t.Fatal("invalid device relay response accepted")
		}
	}
}
