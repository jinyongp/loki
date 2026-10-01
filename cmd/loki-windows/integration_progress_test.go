package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

type integrationProgressRunner struct {
	t      *testing.T
	output *bytes.Buffer
	input  []byte
}

func (r *integrationProgressRunner) Run(_ context.Context, _ string, args []string) (windowshost.NativeProbe, error) {
	if args[len(args)-1] == "version" {
		return windowshost.NativeProbe{Stdout: "loki 1.2.3"}, nil
	}
	return windowshost.NativeProbe{Stdout: `{"generation":{"spec":{"version":"1.2.3"}}}`}, nil
}

func (r *integrationProgressRunner) RunStreaming(_ context.Context, _ string, _ []string, reporter progress.Reporter) (windowshost.NativeProbe, error) {
	progress.Emit(reporter, progress.Event{Message: "Creating a recovery backup..."})
	if !strings.Contains(r.output.String(), "Creating a recovery backup") {
		r.t.Error("progress was withheld until command completion")
	}
	return windowshost.NativeProbe{Stdout: `{"schema_version":1,"ready":true}`}, nil
}

func (r *integrationProgressRunner) RunInputStreaming(ctx context.Context, executable string, args []string, input []byte, reporter progress.Reporter) (windowshost.NativeProbe, error) {
	r.input = append([]byte(nil), input...)
	if strings.Contains(strings.Join(args, " "), string(input)) {
		r.t.Error("private input appeared in argv")
	}
	return r.RunStreaming(ctx, executable, args, reporter)
}

func TestWindowsIntegrationProgressCoversMutationsAndPrivateSetup(t *testing.T) {
	for _, integration := range []string{"browser", "signing", "github"} {
		for _, action := range []string{"enable", "disable", "remove", "setup", "rotate", "doctor"} {
			if integration == "browser" && (action == "remove" || action == "setup" || action == "rotate") {
				continue
			}
			t.Run(integration+"/"+action, func(t *testing.T) {
				var stderr bytes.Buffer
				runner := &integrationProgressRunner{t: t, output: &stderr}
				request := windowshost.OperatorRequest{Command: "integration", Action: action, Integration: integration}
				var input []byte
				if action == "setup" || action == "rotate" {
					input = []byte("synthetic-private-key")
					request.UseStdin = true
					if integration == "signing" {
						request.IdentityName, request.IdentityEmail = "Signing User", "signing@example.test"
					}
				}
				result, err := executeWindowsIntegrationWithProgress(t.Context(), windowshost.OperatorClient{
					WSL: windowshost.WSLClient{Runner: runner},
				}, "loki-mcp", request, input, &stderr)
				if err != nil || result.Probe.Stdout != `{"schema_version":1,"ready":true}` {
					t.Fatalf("result=%#v err=%v", result, err)
				}
				if !bytes.Equal(runner.input, input) || strings.Contains(stderr.String(), "synthetic-private-key") {
					t.Fatal("private input was dropped or leaked into progress")
				}
			})
		}
	}
}
