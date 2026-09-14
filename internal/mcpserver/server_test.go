package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/contract"
)

func testHandlers(t *testing.T) map[string]Handler {
	t.Helper()
	baseline, err := contract.Baseline()
	if err != nil {
		t.Fatal(err)
	}
	defs, err := baseline.Definitions()
	if err != nil {
		t.Fatal(err)
	}
	h := map[string]Handler{}
	for _, d := range defs {
		h[d.Name] = func(_ context.Context, in map[string]any) (*mcp.CallToolResult, error) { return Object(in) }
	}
	return h
}

func connect(t *testing.T, h map[string]Handler) *mcp.ClientSession {
	t.Helper()
	server, err := New(h)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestIncompleteCatalogRejected(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("accepted incomplete implementations")
	}
	h := testHandlers(t)
	delete(h, "task_inspect")
	h["old_task"] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) { return nil, nil }
	if _, err := New(h); err == nil {
		t.Fatal("accepted wrong handler name")
	}
}

func TestExplicitToolCatalogRetainsResources(t *testing.T) {
	definitions := []*mcp.Tool{
		{Name: "one", Description: "First tool.", InputSchema: map[string]any{"type": "object"}},
		{Name: "two", Description: "Second tool.", InputSchema: map[string]any{"type": "object"}},
	}
	handlers := map[string]Handler{
		"one": func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) { return Object(input) },
		"two": func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) { return Object(input) },
	}
	server, err := NewTools(definitions, handlers)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 2 || tools.Tools[0].Name != "one" || tools.Tools[1].Name != "two" {
		t.Fatalf("tools = %#v, error = %v", tools, err)
	}
	resources, err := cs.ListResources(t.Context(), nil)
	if err != nil || len(resources.Resources) != 3 {
		t.Fatalf("resources = %#v, error = %v", resources, err)
	}
	if _, err = NewTools([]*mcp.Tool{definitions[0], definitions[0]}, handlers); err == nil {
		t.Fatal("duplicate definitions accepted")
	}
	if _, err = NewTools(definitions, map[string]Handler{"one": handlers["one"], "wrong": handlers["two"]}); err == nil {
		t.Fatal("mismatched handler names accepted")
	}
}

func TestCatalogAndResourceParity(t *testing.T) {
	cs := connect(t, testHandlers(t))
	ctx := context.Background()
	baseline, _ := contract.Baseline()
	defs, _ := baseline.Definitions()
	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 37 {
		t.Fatalf("tools=%d", len(listed.Tools))
	}
	wanted := map[string]json.RawMessage{}
	for i, d := range defs {
		wanted[d.Name] = baseline.Tools[i]
	}
	for _, tool := range listed.Tools {
		actual, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		json.Unmarshal(actual, &a)
		json.Unmarshal(wanted[tool.Name], &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("schema or metadata changed: %s", tool.Name)
		}
	}
	resources, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Resources) != 3 {
		t.Fatalf("resources=%d", len(resources.Resources))
	}
	for _, r := range resources.Resources {
		result, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: r.URI})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Contents) != 1 || !strings.Contains(result.Contents[0].Text, "<html") {
			t.Errorf("invalid widget %s", r.URI)
		}
	}
	var init mcp.InitializeResult
	json.Unmarshal(baseline.Initialize, &init)
	if cs.InitializeResult().Instructions != init.Instructions {
		t.Error("server instructions changed")
	}
}

func TestArgumentsDefaultsAndErrors(t *testing.T) {
	h := testHandlers(t)
	calls := 0
	h["task_write"] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
		calls++
		return nil, errors.New("private-credential-value")
	}
	cs := connect(t, h)
	ctx := context.Background()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "task_inspect", Arguments: map[string]any{"cwd": "repo", "extra": "ignored"}})
	if err != nil || result.IsError {
		t.Fatalf("defaults: %v %#v", err, result)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var output map[string]any
	json.Unmarshal(data, &output)
	if output["action"] != "list" || output["limit"] != float64(50) || output["status"] != "pending" {
		t.Fatalf("defaults=%v", output)
	}
	if _, ok := output["extra"]; ok {
		t.Fatal("extra argument forwarded")
	}
	for _, args := range []any{map[string]any{}, map[string]any{"cwd": "repo", "action": "oops"}, map[string]any{"cwd": "repo", "action": "add", "fields": "bad"}} {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "task_write", Arguments: args})
		if err != nil || !r.IsError {
			t.Fatalf("accepted invalid arguments: %v %v", r, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid request reached handler")
	}
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "task_write", Arguments: map[string]any{"cwd": "repo", "action": "add"}})
	if err != nil || !r.IsError {
		t.Fatalf("expected safe failure: %v %v", r, err)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "private-credential") {
		t.Fatal("private error leaked")
	}
}

func TestPythonInvalidInputGoldens(t *testing.T) {
	baseline, err := contract.Baseline()
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.InvalidCalls) != 37 {
		t.Fatalf("need 37 error fixtures, got %d", len(baseline.InvalidCalls))
	}
	h := testHandlers(t)
	for name := range h {
		h[name] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
			t.Error("invalid input reached handler")
			return nil, nil
		}
	}
	server, err := New(h)
	if err != nil {
		t.Fatal(err)
	}
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(transport)
	defer httpServer.Close()
	for _, fixture := range baseline.InvalidCalls {
		t.Run(fixture.Tool, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": fixture.Tool, "arguments": fixture.Arguments}})
			req, _ := http.NewRequest("POST", httpServer.URL, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("MCP-Protocol-Version", "2025-11-25")
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var wire struct {
				Result json.RawMessage `json:"result"`
			}
			if err = json.NewDecoder(response.Body).Decode(&wire); err != nil {
				t.Fatal(err)
			}
			var actual, expected any
			json.Unmarshal(wire.Result, &actual)
			json.Unmarshal(fixture.Result, &expected)
			if !reflect.DeepEqual(actual, expected) {
				t.Errorf("error mismatch\nGo: %s\nPython: %s", wire.Result, fixture.Result)
			}
		})
	}
}
