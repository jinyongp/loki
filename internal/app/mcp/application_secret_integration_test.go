package mcpapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/contract"
	"loki/internal/control/appvalues"
	"loki/internal/mcpserver"
	mcptransport "loki/internal/transport/mcp"
)

type applicationMetadataCaller struct{ calls int }

func (c *applicationMetadataCaller) Call(_ context.Context, raw any) (json.RawMessage, error) {
	c.calls++
	request, ok := raw.(appvalues.RPCRequest)
	if !ok || request.Operation != "inspect" || request.Request.Action != "status" || request.Request.Resource == nil || *request.Request.Resource != "example" {
		return nil, errors.New("unexpected application request")
	}
	return json.RawMessage(`{"action":"status","resource":"example","state":"locked","integrity_verified":false,"verification_pending":true}`), nil
}

func TestActiveSecretMCPUsesDedicatedResourceContract(t *testing.T) {
	caller := &applicationMetadataCaller{}
	handlers := mcptransport.ApplicationSecretHandlers(caller)
	definitions, err := contract.CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if handlers[definition.Name] == nil {
			handlers[definition.Name] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return nil, errors.New("out of scope")
			}
		}
	}
	server, err := mcpserver.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "application-contract", Version: "2"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, request := range []map[string]any{
		{"action": "profiles"},
		{"action": "profile", "profile": "github-app"},
		{"action": "status", "resource": "example", "profile": "github-app"},
		{"action": "status", "resource": "example", "env": nil},
	} {
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "secret_inspect", Arguments: request})
		if err != nil || !result.IsError || caller.calls != 0 {
			t.Fatal("legacy/unknown request reached authority", err)
		}
	}
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "secret_inspect", Arguments: map[string]any{"action": "status", "resource": "example"}})
	if err != nil || result.IsError || caller.calls != 1 {
		body, _ := json.Marshal(result)
		t.Fatal("dedicated locked status unavailable", err, caller.calls, string(body))
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "github-app") || strings.Contains(string(encoded), "PRIVATE_KEY") {
		t.Fatal("platform metadata leaked")
	}
}

func TestApplicationTransportUnavailableHasNoRuntimeFallback(t *testing.T) {
	handler := mcptransport.ApplicationSecretHandlers(nil)["secret_inspect"]
	if _, err := handler(t.Context(), map[string]any{"action": "status", "resource": "example"}); err == nil {
		t.Fatal("missing dedicated authority did not fail closed")
	}
}
