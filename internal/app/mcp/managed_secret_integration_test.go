package mcpapp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appruntime "loki/internal/app/runtime"
	"loki/internal/contract"
	"loki/internal/mcpserver"
	"loki/internal/secret"
	mcptransport "loki/internal/transport/mcp"
)

func TestSecretMCPRejectsManagedProfile(t *testing.T) {
	controller := secret.Controller{StateDirectory: filepath.Join(t.TempDir(), "vault")}
	if _, err := controller.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.ManagedCredentials().Set(t.Context(), secret.ManagedGitHubAppPrivateKey, "synthetic-platform"); err != nil {
		t.Fatal(err)
	}

	client := secretSocket(t, appruntime.SecretOperations(controller))
	handlers := mcptransport.SecretHandlers(client)
	definitions, err := contract.CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if handlers[definition.Name] == nil {
			handlers[definition.Name] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return nil, errors.New("unexpected out-of-scope test tool")
			}
		}
	}
	server, err := mcpserver.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "managed-secret-test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	for _, request := range []struct {
		name string
		args map[string]any
	}{
		{"secret_inspect", map[string]any{"action": "profile", "profile": "github-app"}},
		{"secret_write", map[string]any{
			"action": "create_profile", "profile": "github-app", "expected_revision": 2,
			"request_id": "84000000-0000-4000-8000-000000000001",
		}},
		{"secret_write", map[string]any{
			"action": "set_public", "profile": "github-app", "name": "PUBLIC_KEY", "value": "replacement",
			"expected_revision": 2, "request_id": "84000000-0000-4000-8000-000000000002",
		}},
		{"secret_write", map[string]any{
			"action": "generate", "profile": "github-app", "secret": "OTHER",
			"expected_revision": 2, "request_id": "84000000-0000-4000-8000-000000000003",
		}},
		{"secret_delete", map[string]any{
			"action": "secret", "profile": "github-app", "secret": "PRIVATE_KEY",
			"expected_revision": 2, "request_id": "84000000-0000-4000-8000-000000000004",
		}},
		{"secret_delete", map[string]any{
			"action": "profile", "profile": "github-app", "expected_revision": 2,
			"request_id": "84000000-0000-4000-8000-000000000005",
		}},
	} {
		result, callErr := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: request.name, Arguments: request.args})
		if callErr != nil {
			t.Fatal(callErr)
		}
		encoded, _ := json.Marshal(result)
		if !result.IsError || !strings.Contains(string(encoded), "managed platform") {
			t.Fatalf("managed request %s was not rejected: %s", request.name, encoded)
		}
	}

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "secret_inspect", Arguments: map[string]any{"action": "profiles"}})
	if err != nil || result.IsError {
		t.Fatalf("profile discovery failed: %#v, %v", result, err)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	if strings.Contains(string(encoded), "github-app") || strings.Contains(string(encoded), "PRIVATE_KEY") {
		t.Fatalf("managed credential metadata crossed MCP discovery: %s", encoded)
	}
}
