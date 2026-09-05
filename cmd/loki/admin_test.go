package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/rpc"
	"loki/internal/secret"
	"loki/internal/service"
)

type adminCall func(context.Context, any) (json.RawMessage, error)

func (f adminCall) Call(ctx context.Context, request any) (json.RawMessage, error) {
	return f(ctx, request)
}

func TestSecretCLIImportsOnlyDeleteConfirmedSources(t *testing.T) {
	controller := secret.Controller{StateDirectory: filepath.Join(t.TempDir(), "runtime")}
	if _, err := controller.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateProfile(t.Context(), "fixture"); err != nil {
		t.Fatal(err)
	}
	ops := service.SecretOperations(controller)
	realCall := adminCall(func(ctx context.Context, request any) (json.RawMessage, error) {
		m := request.(map[string]any)
		raw, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		result, err := ops[m["operation"].(string)].Handle(ctx, raw)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	})
	var stdout, stderr bytes.Buffer
	input := func(context.Context, io.Writer) (string, error) { return "synthetic-terminal-secret", nil }
	if code := executeAdministrationInput([]string{"secret", "set", "fixture", "TERMINAL"}, realCall, input, &stdout, &stderr); code != 0 {
		t.Fatalf("secret set: %s", &stderr)
	}
	if strings.Contains(stdout.String()+stderr.String(), "synthetic-terminal-secret") {
		t.Fatal("terminal secret disclosed")
	}
	for _, outcome := range []string{"success", "failure", "replacement"} {
		t.Run(outcome, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.env")
			os.WriteFile(path, []byte("TOKEN=synthetic-import-secret\n"), 0600)
			client := adminCall(func(ctx context.Context, request any) (json.RawMessage, error) {
				if outcome == "failure" {
					return nil, errors.New("synthetic failure")
				}
				if outcome == "replacement" {
					os.Rename(path, path+".old")
					os.WriteFile(path, []byte("TOKEN=replacement"), 0600)
				}
				return realCall(ctx, request)
			})
			var stdout, stderr bytes.Buffer
			code := executeAdministration([]string{"secret", "import-env", "fixture", path, "--delete-source"}, client, &stdout, &stderr)
			if outcome == "success" {
				if code != 0 || !strings.Contains(stdout.String(), `"source_deleted": true`) {
					t.Fatalf("import: %d %s %s", code, &stdout, &stderr)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("confirmed source retained")
				}
			} else {
				if code != 1 {
					t.Fatal("failure not surfaced")
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatal("unconfirmed source removed")
				}
			}
			if strings.Contains(stdout.String()+stderr.String(), "synthetic-import-secret") {
				t.Fatal("import secret disclosed")
			}
		})
	}
}

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
