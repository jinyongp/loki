package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"loki/internal/rpc"
	"loki/internal/secret"
	"loki/internal/service"
)

func TestAdministrativeCLIEncryptedRuntime(t *testing.T) {
	controller := secret.Controller{StateDirectory: filepath.Join(t.TempDir(), "runtime")}
	socket := filepath.Join(t.TempDir(), "runtime.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := rpc.Server{AgentUID: uint32(os.Getuid()), Operations: service.SecretOperations(controller), ReadCgroup: func(int32) ([]byte, error) { return []byte("0::/system.slice/loki-mcp.service\n"), nil }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	uid := uint32(os.Getuid())
	client := rpc.Client{Socket: socket, ExpectedUID: &uid}
	for _, args := range [][]string{
		{"secret", "init"},
		{"secret", "profile", "create", "fixture"},
		{"secret", "generate", "fixture", "TOKEN"},
		{"secret", "profile", "show", "fixture"},
		{"action", "set", "fixture", "check", "--cwd", "repo", "--secret", "TOKEN", "--", "npm", "run", "check"},
		{"action", "list", "fixture"},
		{"action", "remove", "fixture", "check"},
		{"secret", "remove", "fixture", "TOKEN"},
		{"secret", "profile", "remove", "fixture"},
	} {
		var stdout, stderr bytes.Buffer
		if code := executeAdministration(args, client, &stdout, &stderr); code != 0 {
			t.Fatalf("%q: %d %s", args, code, &stderr)
		}
		if !json.Valid(stdout.Bytes()) {
			t.Fatalf("invalid output: %s", &stdout)
		}
	}
}
