package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"loki/internal/integrations/github/setup"
	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

type gitHubBrowserProgressRunner struct {
	integrationProgressRunner
	failPoll bool
	warning  string
	actions  []string
}

func (r *gitHubBrowserProgressRunner) response(args []string, input []byte, reporter progress.Reporter) (windowshost.NativeProbe, error) {
	var request githubsetup.Request
	if err := json.Unmarshal(input, &request); err != nil {
		r.t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "one-time-code") {
		r.t.Error("callback code appeared in argv")
	}
	r.actions = append(r.actions, request.Action)
	progress.Emit(reporter, progress.Event{Message: "Running github integration setup; inspecting the current configuration..."})
	if r.failPoll && request.Action == "poll" {
		return windowshost.NativeProbe{ExitCode: 1, Stderr: "[loki] Running github integration setup; inspecting the current configuration...\nselect individual repositories, then rerun setup\n"}, nil
	}
	return windowshost.NativeProbe{Stdout: `{"schema_version":1,"phase":"installation"}`, Stderr: r.warning}, nil
}

func (r *gitHubBrowserProgressRunner) RunInput(_ context.Context, _ string, args []string, input []byte) (windowshost.NativeProbe, error) {
	return r.response(args, input, nil)
}

func (r *gitHubBrowserProgressRunner) RunInputStreaming(_ context.Context, _ string, args []string, input []byte, reporter progress.Reporter) (windowshost.NativeProbe, error) {
	return r.response(args, input, reporter)
}

func TestWindowsGitHubBrowserPollingDoesNotRepeatInspectionProgress(t *testing.T) {
	var stderr bytes.Buffer
	runner := &gitHubBrowserProgressRunner{integrationProgressRunner: integrationProgressRunner{t: t, output: &stderr}}
	transport := windowsGitHubSetupTransport(windowshost.OperatorClient{WSL: windowshost.WSLClient{Runner: runner}}, "loki-mcp", false, &stderr)
	for _, action := range []string{"begin", "exchange", "poll", "poll", "finish", "apply"} {
		if _, err := transport(t.Context(), githubsetup.Request{Action: action, Code: "one-time-code"}); err != nil {
			t.Fatal(err)
		}
		want := 0
		for _, message := range []string{"checking the WSL appliance", "inspecting the current configuration"} {
			if got := strings.Count(stderr.String(), message); got != want {
				t.Fatalf("after %s: %s appeared %d times; want %d; output=%s", action, message, got, want, stderr.String())
			}
		}
	}
	if len(runner.actions) != 6 || strings.Contains(stderr.String(), "one-time-code") {
		t.Fatal("requests were skipped or callback code leaked")
	}
}

func TestWindowsGitHubBrowserQuietPollPreservesFailure(t *testing.T) {
	var stderr bytes.Buffer
	runner := &gitHubBrowserProgressRunner{integrationProgressRunner: integrationProgressRunner{t: t, output: &stderr}, failPoll: true}
	transport := windowsGitHubSetupTransport(windowshost.OperatorClient{WSL: windowshost.WSLClient{Runner: runner}}, "loki-mcp", false, &stderr)
	_, err := transport(t.Context(), githubsetup.Request{Action: "poll"})
	if err == nil || err.Error() != "select individual repositories, then rerun setup" || stderr.Len() != 0 {
		t.Fatalf("err=%v progress=%q", err, stderr.String())
	}
}

func TestWindowsGitHubBrowserCompactOutputPreservesWarnings(t *testing.T) {
	var stderr bytes.Buffer
	runner := &gitHubBrowserProgressRunner{integrationProgressRunner: integrationProgressRunner{t: t, output: &stderr}, warning: "[loki] Inspecting current configuration...\nactual integration warning\n"}
	transport := windowsGitHubSetupTransport(windowshost.OperatorClient{WSL: windowshost.WSLClient{Runner: runner}}, "loki-mcp", false, &stderr)
	if _, err := transport(t.Context(), githubsetup.Request{Action: "begin"}); err != nil || stderr.String() != "actual integration warning\n" {
		t.Fatalf("err=%v output=%q", err, stderr.String())
	}
}
