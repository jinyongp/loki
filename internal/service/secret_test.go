package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/contract"
	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
	"loki/internal/secret"
	"loki/internal/state"
)

func secretSocket(t *testing.T, ops map[string]rpc.Operation, socketPaths ...string) rpc.Client {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "secret.sock")
	if len(socketPaths) > 0 {
		socket = socketPaths[0]
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	server := rpc.Server{Principals: identity.UnixResolver{AgentUID: uint32(os.Getuid())}, Operations: ops}
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	uid := uint32(os.Getuid())
	return rpc.Client{Socket: socket, ExpectedUID: &uid}
}
func TestRuntimeTypedRejectsUnknownFieldsBeforeMutation(t *testing.T) {
	controller := secret.Controller{StateDirectory: filepath.Join(t.TempDir(), "runtime")}
	if _, err := controller.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	op := SecretOperations(controller)["profile_create"]
	if _, err := op.Handle(t.Context(), json.RawMessage(`{"operation":"profile_create","profile":"web","profiel":"typo"}`)); err == nil {
		t.Fatal("unknown runtime field reached profile mutation")
	}
	profiles, err := controller.Profiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles["profiles"].([]map[string]any)) != 0 {
		t.Fatal("invalid runtime request mutated the vault")
	}
	if _, err = op.Handle(t.Context(), json.RawMessage(`{"operation":"profile_create","profile":"web"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestSecretAndWorkflowMCP(t *testing.T) {
	c := secret.Controller{StateDirectory: filepath.Join(t.TempDir(), "runtime")}
	if _, err := c.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	ops := SecretOperations(c)
	client := secretSocket(t, ops)
	for _, name := range []string{"init", "import_env", "secret_set"} {
		if ops[name].Grant != controlpolicy.HostAdministration {
			t.Fatalf("administrative operation exposed: %s", name)
		}
	}
	for _, name := range []string{"public_value_set", "secret_generate", "import_staged_env"} {
		if ops[name].Grant != controlpolicy.Agent {
			t.Fatalf("delegated operation changed: %s", name)
		}
	}
	handlers := SecretHandlers(client)
	defs, _ := contract.CurrentDefinitions()
	for _, def := range defs {
		if handlers[def.Name] == nil {
			handlers[def.Name] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return nil, errors.New("unexpected out-of-scope test tool")
			}
		}
	}
	server, err := mcpserver.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "isolated-secret-test", Version: "1"}, nil).Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	var responses [][]byte
	call := func(name string, args map[string]any, wantedError string) map[string]any {
		t.Helper()
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(result)
		responses = append(responses, data)
		if wantedError != "" {
			if !result.IsError || !bytes.Contains(data, []byte(wantedError)) {
				t.Fatalf("wanted %s: %s", wantedError, data)
			}
			return nil
		}
		if result.IsError {
			t.Fatalf("%s: %s", name, data)
		}
		data, _ = json.Marshal(result.StructuredContent)
		var out map[string]any
		if err = json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	call("secret_write", map[string]any{"action": "create_profile", "profile": "web"}, "")
	generated := call("secret_write", map[string]any{"action": "generate", "profile": "web", "secret": "TOKEN"}, "")
	if generated["bytes"] != float64(32) {
		t.Fatal("schema default byte count changed")
	}
	call("secret_write", map[string]any{"action": "set", "profile": "web", "secret": "PUBLIC_API", "value": "http://127.0.0.1:41280"}, "")
	profiles := call("secret_inspect", map[string]any{"action": "profiles", "offset": 0, "limit": 1}, "")
	profile := call("secret_inspect", map[string]any{"action": "profile", "profile": "web"}, "")
	if profiles["revision"] != profile["revision"] || profiles["total"] != float64(1) ||
		profiles["has_more"] != false || profiles["next_offset"] != nil || profiles["complete"] != true {
		t.Fatalf("secret metadata pagination/revision = profiles=%#v profile=%#v", profiles, profile)
	}
	foundToken := false
	for _, name := range profile["secret_names"].([]any) {
		foundToken = foundToken || name == "TOKEN"
	}
	if !foundToken {
		t.Fatal("generated secret missing from profile")
	}
	inbox := filepath.Join(c.StateDirectory, "inbox")
	if err = os.Mkdir(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef0123456789abcdef"
	source := filepath.Join(inbox, id+".env")
	if err = os.WriteFile(source, []byte("IMPORTED=synthetic-private-import\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call("secret_inspect", map[string]any{"action": "imports"}, "")
	call("secret_write", map[string]any{"action": "import_env", "profile": "web", "import_id": id}, "")
	call("secret_inspect", map[string]any{"action": "profile", "profile": "web"}, "")
	snapshot, err := (state.Store{Dir: c.StateDirectory, Validate: secret.Validate}).Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var private struct {
		Profiles map[string]struct {
			Secrets map[string]string `json:"secrets"`
		} `json:"profiles"`
	}
	if err = json.Unmarshal(snapshot.Data, &private); err != nil {
		t.Fatal(err)
	}
	for _, data := range responses {
		if bytes.Contains(data, []byte(private.Profiles["web"].Secrets["TOKEN"])) || bytes.Contains(data, []byte("synthetic-private-import")) {
			t.Fatal("secret value crossed MCP response boundary")
		}
	}
	if os.Getuid() != 0 {
		untrusted := secretSocket(t, ops)
		for _, operation := range []string{"secret_set"} {
			_, err := untrusted.Call(t.Context(), map[string]any{"operation": operation, "cwd": "repo", "profile": "web", "secret": "TOKEN", "value": "should-not-be-stored"})
			if err == nil || !strings.Contains(err.Error(), "administrative operations") {
				t.Fatalf("untrusted administrative call %s: %v", operation, err)
			}
		}
		if _, err = untrusted.Call(t.Context(), map[string]any{"operation": "get_profile", "profile": "web"}); err != nil {
			t.Fatalf("delegated metadata read: %v", err)
		}
	}
}
