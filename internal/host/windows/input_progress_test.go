package windows

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"loki/internal/progress"
)

func TestNativeInputStreamingReportsBeforeProcessExits(t *testing.T) {
	for _, exitCode := range []int{0, 9} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			gate := filepath.Join(t.TempDir(), "progress-received")
			t.Setenv("LOKI_TEST_INPUT_PROGRESS_GATE", gate)
			var output bytes.Buffer
			var callbackErr error
			lineReporter := progress.NewLineReporter(progress.WithVerbose(&output))
			reporter := progress.ReporterFunc(func(event progress.Event) {
				lineReporter.Report(event)
				callbackErr = os.WriteFile(gate, []byte("ready"), 0600)
			})
			probe, err := (ExecNativeRunner{}).RunInputStreaming(t.Context(), binary,
				[]string{"-test.run=^TestNativeInputProgressChild$", "--", strconv.Itoa(exitCode)},
				[]byte("synthetic-private-key"), reporter)
			if err != nil || callbackErr != nil || probe.ExitCode != exitCode {
				t.Fatalf("exit=%d err=%v callbackErr=%v stderr=%s", probe.ExitCode, err, callbackErr, probe.Stderr)
			}
			if output.String() != "[loki] Backing up managed integration credentials...\n" || probe.Stdout != `{"configured":true}` {
				t.Fatalf("progress=%q stdout=%q", output.String(), probe.Stdout)
			}
			if strings.Contains(output.String()+probe.Stdout+probe.Stderr, "synthetic-private-key") {
				t.Fatal("private stdin appeared in process output")
			}
		})
	}
}

func TestNativeInputProgressChild(t *testing.T) {
	gate := os.Getenv("LOKI_TEST_INPUT_PROGRESS_GATE")
	if gate == "" {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil || string(input) != "synthetic-private-key" {
		os.Exit(7)
	}
	fmt.Fprintln(os.Stderr, "[loki] Backing up managed integration credentials...")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = os.Stat(gate); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(8)
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Fprintln(os.Stdout, `{"configured":true}`)
	code, _ := strconv.Atoi(os.Args[len(os.Args)-1])
	if code != 0 {
		fmt.Fprintln(os.Stderr, "synthetic operation failure")
	}
	os.Exit(code)
}

func TestOperatorInputProgressKeepsCredentialsOnStdin(t *testing.T) {
	for _, integration := range []string{"github", "signing"} {
		t.Run(integration, func(t *testing.T) {
			runner := &fakeNativeRunner{results: []NativeProbe{
				{Stdout: ownedManifest("1.2.3")}, {Stdout: "loki 1.2.3"},
				{Stdout: "configured", Stderr: "[loki] Preparing recovery backup...\n"},
			}}
			client := OperatorClient{WSL: WSLClient{Runner: runner}}
			secret := []byte("synthetic-private-key")
			var output bytes.Buffer
			request := OperatorRequest{
				Command: "integration", Action: "rotate", Integration: integration, UseStdin: true,
			}
			if integration == "signing" {
				request.IdentityName, request.IdentityEmail = "Signing User", "signing@example.test"
			}
			result, err := client.ExecuteInputStreaming(t.Context(), "loki-mcp", request, secret, progress.NewLineReporter(progress.WithVerbose(&output)))
			if err != nil {
				t.Fatal(err)
			}
			call := runner.calls[len(runner.calls)-1]
			if !bytes.Equal(call.input, secret) || strings.Contains(strings.Join(call.arguments, " ")+output.String(), string(secret)) {
				t.Fatal("private key was not confined to stdin")
			}
			if result.Probe.Stdout != "configured" || output.String() != "[loki] Preparing recovery backup...\n" {
				t.Fatalf("result=%#v progress=%q", result, output.String())
			}
		})
	}
}
