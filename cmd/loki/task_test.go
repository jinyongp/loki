package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"loki/internal/rpc"
)

func TestTaskWrapperSocketAndWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	socket := filepath.Join(t.TempDir(), "runtime.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	requests := make(chan map[string]any, 4)
	server := rpc.Server{AgentUID: uint32(os.Getuid()), Operations: map[string]rpc.Operation{"project_task": {Handle: func(_ context.Context, raw json.RawMessage) (any, error) {
		var request map[string]any
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		requests <- request
		return map[string]any{"exit_code": 7, "output": "작업 결과\n"}, nil
	}}}}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	args := []string{"internal", "task", socket, strconv.Itoa(os.Getuid()), root, "project:go", "list"}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 7 || stdout.String() != "작업 결과\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
	request := <-requests
	if request["cwd"] != "repo" || request["timeout_seconds"] != float64(900) || len(request["arguments"].([]any)) != 2 {
		t.Fatalf("request %v", request)
	}
	for _, tail := range [][]string{{"exec", "/usr/bin/true"}, {"rc:/dev/null"}} {
		if code := run(append(args[:5:5], tail...), &stdout, &stderr); code != 2 {
			t.Fatal("unsafe command accepted")
		}
	}
	outside := append([]string{}, args...)
	outside[4] = repo + "-other"
	if err := os.Mkdir(outside[4], 0700); err != nil {
		t.Fatal(err)
	}
	if code := run(outside, &stdout, &stderr); code != 1 {
		t.Fatal("outside workspace accepted")
	}
	select {
	case request := <-requests:
		t.Fatalf("unexpected request %v", request)
	default:
	}
}
