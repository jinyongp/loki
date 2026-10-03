package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"loki/internal/integrations/github/setup"
	windowshost "loki/internal/host/windows"
)

type githubUserRelayRunner struct {
	integrationProgressRunner
	response string
	detail   string
	exitCode int
	input    []byte
}

func (r *githubUserRelayRunner) RunInput(_ context.Context, _ string, args []string, input []byte) (windowshost.NativeProbe, error) {
	if !strings.Contains(strings.Join(args, " "), "host integration login --system --browser-request github") || strings.Contains(strings.Join(args, " "), "session-marker") {
		r.t.Fatal("invalid device relay argv", args)
	}
	r.input = append([]byte(nil), input...)
	return windowshost.NativeProbe{Stdout: r.response, Stderr: r.detail, ExitCode: r.exitCode}, nil
}

func TestWindowsGitHubUserRelayPreservesSanitizedRuntimeAdvice(t *testing.T) {
	var output bytes.Buffer
	runner := &githubUserRelayRunner{integrationProgressRunner: integrationProgressRunner{t: t, output: &output}, exitCode: 1}
	transport := windowsGitHubUserTransport(windowshost.OperatorClient{WSL: windowshost.WSLClient{Runner: runner}}, "loki-mcp")
	for _, advice := range []string{"enable Device flow in the GitHub App settings, then retry setup", "GitHub authorization request timed out; retry setup", "GitHub authorization request was rejected"} {
		// Native host relay has already sanitized the runtime diagnostic.
		runner.detail = "[loki] Checking authorization...\nloki: " + githubsetup.UserLoginError("loki: "+advice).Error() + "\n"
		if _, err := transport(t.Context(), githubsetup.UserRequest{Action: "begin"}); err == nil || err.Error() != advice {
			t.Fatal("Windows relay lost safe advice", err)
		}
	}
	runner.detail = "loki: ghu_private-device-code"
	if _, err := transport(t.Context(), githubsetup.UserRequest{Action: "begin"}); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("Windows relay exposed private diagnostics", err)
	}
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
