package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/contract"
)

func testHandlers(t *testing.T) map[string]Handler {
	t.Helper()
	definitions, err := contract.CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	handlers := map[string]Handler{}
	for _, definition := range definitions {
		handlers[definition.Name] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) { return Object(input) }
	}
	return handlers
}

func connect(t *testing.T, handlers map[string]Handler) *mcp.ClientSession {
	t.Helper()
	server, err := New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	session, err := server.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestCurrentCatalogAndResources(t *testing.T) {
	handlers := testHandlers(t)
	delete(handlers, "workspace_read")
	if _, err := New(handlers); err == nil {
		t.Fatal("accepted incomplete catalog")
	}
	client := connect(t, testHandlers(t))
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil || len(listed.Tools) != 27 {
		t.Fatal(listed, err)
	}
	current, _ := contract.Current()
	wanted := map[string]json.RawMessage{}
	definitions, _ := current.Definitions()
	for index, definition := range definitions {
		wanted[definition.Name] = current.Tools[index]
	}
	for _, tool := range listed.Tools {
		actual, _ := json.Marshal(tool)
		var left, right any
		json.Unmarshal(actual, &left)
		json.Unmarshal(wanted[tool.Name], &right)
		if !reflect.DeepEqual(left, right) {
			t.Errorf("schema changed: %s", tool.Name)
		}
	}
	resources, err := client.ListResources(t.Context(), nil)
	if err != nil || len(resources.Resources) != 3 {
		t.Fatal(resources, err)
	}
}

func TestSchemaValidationAndSafeErrors(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["preview_publish"] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
		calls++
		return nil, errors.New("private-credential-value")
	}
	client := connect(t, handlers)
	for _, arguments := range []map[string]any{{}, {"action": "oops"}, {"action": "server", "port": "bad"}} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "preview_publish", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("accepted invalid arguments: %#v %v", result, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid input reached handler")
	}
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "preview_publish", Arguments: map[string]any{"action": "server", "port": 43000}})
	if err != nil || !result.IsError {
		t.Fatal(result, err)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "private-credential") {
		t.Fatal("private error leaked")
	}
}
