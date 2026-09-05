package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"loki/internal/rpc"
)

func TestVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(version) = %d, stderr = %q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "loki 0.48.0-dev" {
		t.Fatalf("version = %q", got)
	}
}

func TestBootstrapSocket(t *testing.T) {
	for _, exitCode := range []int{0, 17} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "runtime.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			server := rpc.Server{AgentUID: uint32(os.Getuid()), Operations: map[string]rpc.Operation{
				"project_workflow": {Handle: func(context.Context, json.RawMessage) (any, error) {
					return map[string]any{"steps": [][]string{{"web", "setup"}}}, nil
				}},
				"run_action": {Handle: func(context.Context, json.RawMessage) (any, error) {
					return map[string]any{"session_id": "test-session"}, nil
				}},
				"read_process": {Handle: func(context.Context, json.RawMessage) (any, error) {
					return map[string]any{"status": "exited", "exit_code": exitCode, "output": "safe output", "next_offset": 11}, nil
				}},
			}}
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx, listener) }()
			t.Cleanup(func() { cancel(); listener.Close(); <-done })
			var stdout, stderr bytes.Buffer
			args := []string{"internal", "bootstrap", socket, strconv.Itoa(os.Getuid()), ".", "development"}
			if code := run(args, &stdout, &stderr); code != exitCode {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "safe output") {
				t.Fatal(stdout.String())
			}
			args[3] = strconv.Itoa(os.Getuid() + 1)
			stdout.Reset()
			if code := run(args, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
				t.Fatalf("wrong UID accepted: %d %s", code, stdout.String())
			}
		})
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run(unknown) = %d", code)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
