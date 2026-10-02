package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

type integrationProgressRunner struct {
	t       *testing.T
	output  interface{ String() string }
	input   []byte
	verbose bool
	signing bool
}

func (r *integrationProgressRunner) Run(_ context.Context, _ string, args []string) (windowshost.NativeProbe, error) {
	if r.signing && !strings.Contains(r.output.String(), "Setting up Git commit signing") {
		r.t.Error("signing setup did not announce work before the WSL inspection")
	}
	if args[len(args)-1] == "version" {
		return windowshost.NativeProbe{Stdout: "loki 1.2.3"}, nil
	}
	return windowshost.NativeProbe{Stdout: `{"generation":{"spec":{"version":"1.2.3"}}}`}, nil
}

func (r *integrationProgressRunner) RunStreaming(_ context.Context, _ string, _ []string, reporter progress.Reporter) (windowshost.NativeProbe, error) {
	progress.Emit(reporter, progress.Event{Message: "Creating a recovery backup..."})
	if strings.Contains(r.output.String(), "Creating a recovery backup") != r.verbose {
		r.t.Error("internal progress did not follow the selected verbosity")
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
				runner := &integrationProgressRunner{t: t, output: &stderr, signing: integration == "signing" && (action == "setup" || action == "rotate")}
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
				if runner.signing && strings.Count(stderr.String(), "Setting up Git commit signing") != 1 {
					t.Fatalf("signing start notice was missing or repeated: %s", &stderr)
				}
			})
		}
	}
}

type waitingSigningProgressRunner struct {
	integrationProgressRunner
	entered, resume chan struct{}
	waited          bool
}

type signingProgressBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *signingProgressBuffer) Write(raw []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(raw)
}

func (buffer *signingProgressBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func (r *waitingSigningProgressRunner) Run(ctx context.Context, executable string, args []string) (windowshost.NativeProbe, error) {
	if !r.waited {
		r.waited = true
		close(r.entered)
		select {
		case <-r.resume:
		case <-ctx.Done():
			return windowshost.NativeProbe{}, ctx.Err()
		}
	}
	return r.integrationProgressRunner.Run(ctx, executable, args)
}

func (r *waitingSigningProgressRunner) RunStreaming(context.Context, string, []string, progress.Reporter) (windowshost.NativeProbe, error) {
	return windowshost.NativeProbe{Stdout: `{"schema_version":1,"ready":true}`}, nil
}

func TestSigningSetupAnnouncesWorkAndLongWaitBeforeWSLReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var stderr signingProgressBuffer
		runner := &waitingSigningProgressRunner{
			integrationProgressRunner: integrationProgressRunner{t: t, output: &stderr},
			entered:                   make(chan struct{}), resume: make(chan struct{}),
		}
		done := make(chan error, 1)
		go func() {
			_, err := executeWindowsIntegrationWithProgress(t.Context(), windowshost.OperatorClient{
				WSL: windowshost.WSLClient{Runner: runner},
			}, "loki-mcp", windowshost.OperatorRequest{
				Command: "integration", Action: "setup", Integration: "signing",
				IdentityName: "Signing Fixture", IdentityEmail: "signing@example.test",
			}, nil, &stderr)
			done <- err
		}()
		<-runner.entered
		synctest.Wait()
		if !strings.Contains(stderr.String(), "Setting up Git commit signing") {
			t.Error("signing work was not announced while the WSL inspection was blocked")
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if !strings.Contains(stderr.String(), "Still setting up Git commit signing (30s elapsed)") {
			t.Errorf("signing wait notice missing: %s", &stderr)
		}
		close(runner.resume)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestWindowsVerboseIntegrationStreamsDetailsBeforeCompletion(t *testing.T) {
	var stderr bytes.Buffer
	runner := &integrationProgressRunner{t: t, output: &stderr, verbose: true}
	_, err := executeWindowsIntegrationWithProgress(t.Context(), windowshost.OperatorClient{WSL: windowshost.WSLClient{Runner: runner}}, "loki-mcp", windowshost.OperatorRequest{Command: "integration", Action: "enable", Integration: "github"}, nil, progress.WithVerbose(&stderr))
	if err != nil || !strings.Contains(stderr.String(), "Creating a recovery backup") || !strings.Contains(stderr.String(), "checking the WSL appliance") {
		t.Fatalf("verbose output=%q err=%v", stderr.String(), err)
	}
}

func TestGitHubDoctorAnnouncesWorkBeforeWSLReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var stderr signingProgressBuffer
		runner := &waitingSigningProgressRunner{
			integrationProgressRunner: integrationProgressRunner{t: t, output: &stderr},
			entered:                   make(chan struct{}), resume: make(chan struct{}),
		}
		done := make(chan error, 1)
		go func() {
			_, err := executeWindowsIntegrationWithProgress(t.Context(), windowshost.OperatorClient{
				WSL: windowshost.WSLClient{Runner: runner},
			}, "loki-mcp", windowshost.OperatorRequest{Command: "integration", Action: "doctor", Integration: "github"}, nil, &stderr)
			done <- err
		}()
		<-runner.entered
		synctest.Wait()
		if !strings.Contains(stderr.String(), "Running github integration doctor; checking the WSL appliance...") {
			t.Error("GitHub doctor remained silent during WSL inspection")
		}
		time.Sleep(45 * time.Second)
		synctest.Wait()
		if !strings.Contains(stderr.String(), "Still waiting for github integration doctor (45s elapsed)") {
			t.Error("GitHub doctor long wait notice missing", stderr.String())
		}
		close(runner.resume)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
