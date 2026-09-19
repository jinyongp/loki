package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/contract"
	"loki/internal/fault"
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
	if err != nil || len(listed.Tools) != 32 {
		t.Fatal(listed, err)
	}
	wanted := map[string]json.RawMessage{}
	definitions, _ := contract.CurrentDefinitions()
	for _, definition := range definitions {
		raw, _ := json.Marshal(definition)
		wanted[definition.Name] = raw
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

func TestToolErrorsExposeTypedPublicEnvelope(t *testing.T) {
	handlers := testHandlers(t)
	handlers["preview_publish"] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
		return nil, fault.New(fault.CodeConflict, "preview state changed", true, "inspect current preview state before retrying")
	}
	client := connect(t, handlers)
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "preview_publish", Arguments: map[string]any{"action": "server", "port": 43000},
	})
	if err != nil || !result.IsError {
		t.Fatal(result, err)
	}
	if result.StructuredContent != nil {
		t.Fatalf("error structured content must not bypass the tool output schema: %#v", result.StructuredContent)
	}
	public, ok := result.Meta["loki/error"].(map[string]any)
	if !ok {
		t.Fatalf("public error metadata = %#v", result.Meta)
	}
	if public["code"] != string(fault.CodeConflict) || public["message"] != "preview state changed" || public["retryable"] != true {
		t.Fatalf("public error = %#v", public)
	}
	if public["next_action"] != "inspect current preview state before retrying" {
		t.Fatalf("next action = %#v", public)
	}
}

func TestSystemInspectOperationSchemaPreservesDefaultAndRejectsIrrelevantFields(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["system_inspect"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		return Object(input)
	}
	client := connect(t, handlers)

	for _, arguments := range []map[string]any{
		{},
		{"action": "activity", "limit": 3},
		{"action": "operation", "correlation_id": "0123456789abcdef-0000000000000001"},
		{"action": "port", "port": 43000},
	} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "system_inspect", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid system_inspect call failed: %#v %v", result, err)
		}
	}
	if calls != 4 {
		t.Fatalf("valid calls = %d", calls)
	}

	for _, arguments := range []map[string]any{
		{"action": "server", "port": 43000},
		{"action": "activity", "correlation_id": "0123456789abcdef-0000000000000001"},
		{"action": "operation", "correlation_id": "0123456789abcdef-0000000000000001", "limit": 3},
		{"action": "port", "port": 43000, "limit": 3},
	} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "system_inspect", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("irrelevant system_inspect field was accepted: %#v %v", result, err)
		}
	}
	if calls != 4 {
		t.Fatal("invalid system_inspect input reached handler")
	}
}

func TestUnknownToolArgumentsNeverReachHandlers(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["git_stage"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		return Object(input)
	}
	client := connect(t, handlers)
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "git_stage",
		Arguments: map[string]any{
			"action":               "paths",
			"paths":                []string{"fixture.txt"},
			"expected_index_sha25": "misspelled-not-a-real-index",
		},
	})
	if err != nil || !result.IsError {
		t.Fatalf("unknown field was accepted: %#v %v", result, err)
	}
	if calls != 0 {
		t.Fatal("unknown tool input reached handler")
	}
}

func TestGitHubIssueFieldsRejectsCredentialAndTransportArguments(t *testing.T) {
	client := connect(t, testHandlers(t))
	cases := []struct {
		tool      string
		arguments map[string]any
	}{
		{tool: "github_issue_fields_read", arguments: map[string]any{"action": "list_fields", "target": "owner/repo"}},
		{tool: "github_issue_fields_write", arguments: map[string]any{"action": "clear_value", "target": "owner/repo", "issue": 1, "field_id": 2}},
	}
	for _, test := range cases {
		for _, key := range []string{"token", "url", "headers", "method"} {
			arguments := make(map[string]any, len(test.arguments)+1)
			for name, value := range test.arguments {
				arguments[name] = value
			}
			arguments[key] = "private"
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.tool, Arguments: arguments})
			if err != nil || !result.IsError {
				t.Fatalf("%s accepted %s: %#v %v", test.tool, key, result, err)
			}
		}
	}
}

func TestGitHubIssueFieldsReadWriteSchemasAreDisjoint(t *testing.T) {
	handlers := testHandlers(t)
	calls := map[string]int{}
	for _, name := range []string{"github_issue_fields_read", "github_issue_fields_write"} {
		toolName := name
		handlers[toolName] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
			calls[toolName]++
			return Object(input)
		}
	}
	client := connect(t, handlers)

	valid := []struct {
		tool string
		args map[string]any
	}{
		{tool: "github_issue_fields_read", args: map[string]any{"action": "list_fields", "target": "owner/repo"}},
		{tool: "github_issue_fields_read", args: map[string]any{"action": "list_values", "target": "owner/repo", "issue": 3}},
		{
			tool: "github_issue_fields_write",
			args: map[string]any{
				"action": "add_values", "target": "owner/repo", "issue": 3,
				"values": []any{map[string]any{"field_id": 1, "data_type": "text", "text": "x"}},
			},
		},
		{tool: "github_issue_fields_write", args: map[string]any{"action": "clear_value", "target": "owner/repo", "issue": 3, "field_id": 1}},
	}
	for _, test := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.tool, Arguments: test.args})
		if err != nil || result.IsError {
			t.Fatalf("valid %s rejected: args=%#v result=%#v err=%v", test.tool, test.args, result, err)
		}
	}

	invalid := []struct {
		tool string
		args map[string]any
	}{
		{tool: "github_issue_fields_read", args: map[string]any{"action": "add_values", "target": "owner/repo", "issue": 3}},
		{tool: "github_issue_fields_read", args: map[string]any{"action": "list_fields", "target": "owner/repo", "issue": 3}},
		{tool: "github_issue_fields_read", args: map[string]any{"action": "list_values", "target": "owner/repo"}},
		{tool: "github_issue_fields_write", args: map[string]any{"action": "list_fields", "target": "owner/repo"}},
		{
			tool: "github_issue_fields_write",
			args: map[string]any{"action": "clear_value", "target": "owner/repo", "issue": 3, "field_id": 1, "values": []any{}},
		},
	}
	for _, test := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.tool, Arguments: test.args})
		if err != nil || !result.IsError {
			t.Fatalf("invalid %s accepted: args=%#v result=%#v err=%v", test.tool, test.args, result, err)
		}
	}
	if calls["github_issue_fields_read"] != 2 || calls["github_issue_fields_write"] != 2 {
		t.Fatalf("invalid Issue Fields calls reached handlers: %#v", calls)
	}
}

func TestGitHubCommandRejectsCredentialAndScopeArguments(t *testing.T) {
	client := connect(t, testHandlers(t))
	for _, key := range []string{"token", "headers", "environment", "cwd"} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "github",
			Arguments: map[string]any{"target": "owner/repo", "args": []string{"issue", "list"}, key: "private"},
		})
		if err != nil || !result.IsError {
			t.Fatalf("accepted %s: %#v %v", key, result, err)
		}
	}
}

func TestHandlerContextCarriesStableServerSessionID(t *testing.T) {
	handlers := testHandlers(t)
	var mu sync.Mutex
	seen := []string{}
	handlers["system_inspect"] = func(ctx context.Context, _ map[string]any) (*mcp.CallToolResult, error) {
		id, ok := SessionID(ctx)
		if !ok {
			id = ""
		}
		mu.Lock()
		seen = append(seen, id)
		mu.Unlock()
		return Object(map[string]any{"ok": true})
	}
	server, err := New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:    false,
			JSONResponse: true,
		},
	))
	defer httpServer.Close()

	connectClient := func(name string) *mcp.ClientSession {
		client, err := mcp.NewClient(&mcp.Implementation{Name: name, Version: "1"}, nil).Connect(
			t.Context(),
			&mcp.StreamableClientTransport{Endpoint: httpServer.URL, DisableStandaloneSSE: true},
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { client.Close() })
		return client
	}
	call := func(client *mcp.ClientSession) {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "system_inspect", Arguments: map[string]any{"action": "server"}})
		if err != nil || result.IsError {
			t.Fatalf("call failed: %#v %v", result, err)
		}
	}
	first := connectClient("first")
	call(first)
	call(first)
	second := connectClient("second")
	call(second)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 || seen[0] == "" || seen[0] != seen[1] || seen[2] == "" || seen[2] == seen[0] {
		t.Fatalf("session ids = %#v", seen)
	}
	if _, ok := SessionID(context.Background()); ok {
		t.Fatal("session id appeared outside a server request context")
	}
}

func TestStreamableHTTPFailuresAreTerminalAndSessionReusable(t *testing.T) {
	handlers := testHandlers(t)
	handlers["preview_publish"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		port, _ := input["port"].(float64)
		if port == 43001 {
			return nil, errors.New("private-handler-detail")
		}
		if port == 43002 {
			panic("private-panic-detail")
		}
		return Object(map[string]any{"ok": true})
	}
	server, err := New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:    false,
			JSONResponse: true,
		},
	))
	defer httpServer.Close()

	client, err := mcp.NewClient(&mcp.Implementation{Name: "terminal-lifecycle", Version: "1"}, nil).Connect(
		t.Context(),
		&mcp.StreamableClientTransport{Endpoint: httpServer.URL, DisableStandaloneSSE: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	recoverSession := func(label string) {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "system_inspect",
			Arguments: map[string]any{"action": "server"},
		})
		if err != nil || result.IsError {
			t.Fatalf("%s left MCP session unusable: result=%#v err=%v", label, result, err)
		}
	}
	expectTerminalError := func(label string, arguments map[string]any) {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "preview_publish",
			Arguments: arguments,
		})
		if err != nil {
			t.Fatalf("%s became a transport/RPC error: %v", label, err)
		}
		if result == nil || !result.IsError {
			t.Fatalf("%s did not return a terminal tool error: %#v", label, result)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "private-handler-detail") || strings.Contains(string(encoded), "private-panic-detail") {
			t.Fatalf("%s leaked private failure detail: %s", label, encoded)
		}
		recoverSession(label)
	}

	expectTerminalError("validation failure", map[string]any{})
	expectTerminalError("handler failure", map[string]any{"action": "server", "port": 43001})
	expectTerminalError("panic failure", map[string]any{"action": "server", "port": 43002})
}

func TestWorkspaceEditActionSchemaRejectsIrrelevantFieldsBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["workspace_edit"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		return Object(input)
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{"action": "create", "path": "new.txt", "content": ""},
		{
			"action": "replace", "path": "file.txt", "old": "before", "new": "after",
			"expected_sha256": strings.Repeat("a", 64), "expected_replacements": 1,
		},
		{"action": "patch", "patch": "diff --git a/a.txt b/a.txt\n"},
		{"action": "move", "source": "a.txt", "destination": "b.txt"},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "workspace_edit", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid workspace_edit rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid workspace_edit calls = %d, want %d", calls, len(valid))
	}

	invalid := []map[string]any{
		{"action": "create", "path": "new.txt", "content": "", "expected_sha256": strings.Repeat("a", 64)},
		{"action": "replace", "path": "file.txt", "old": "before", "new": "after", "expected_replacements": 1},
		{
			"action": "replace", "path": "file.txt", "old": "before", "new": "after",
			"expected_sha256": strings.Repeat("a", 64), "expected_replacements": 0,
		},
		{"action": "patch", "patch": "diff --git a/a.txt b/a.txt\n", "path": "a.txt"},
		{"action": "move", "source": "a.txt", "destination": "b.txt", "patch": "diff"},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "workspace_edit", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid workspace_edit accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid workspace_edit reached handler: calls=%d want=%d", calls, len(valid))
	}
}

func TestGitStageActionSchemaRequiresCASAndRejectsIrrelevantFields(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["git_stage"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		return Object(input)
	}
	client := connect(t, handlers)
	expected := strings.Repeat("b", 64)

	valid := []map[string]any{
		{"action": "paths", "paths": []string{"a.txt", "b.txt"}, "expected_index_sha256": expected},
		{"action": "unstage", "cwd": "repo", "paths": []string{"a.txt"}, "expected_index_sha256": expected},
		{
			"action": "patch", "patch": "diff --git a/a.txt b/a.txt\n",
			"reverse": true, "expected_index_sha256": expected,
		},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "git_stage", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid git_stage rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid git_stage calls = %d, want %d", calls, len(valid))
	}

	invalid := []map[string]any{
		{"action": "paths", "paths": []string{"a.txt"}},
		{"action": "paths", "paths": []string{"a.txt"}, "reverse": true, "expected_index_sha256": expected},
		{"action": "unstage", "paths": []string{"a.txt"}, "patch": "diff", "expected_index_sha256": expected},
		{"action": "patch", "patch": "diff", "paths": []string{"a.txt"}, "expected_index_sha256": expected},
		{"action": "patch", "patch": "diff", "expected_index_sha256": "not-a-digest"},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "git_stage", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid git_stage accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid git_stage reached handler: calls=%d want=%d", calls, len(valid))
	}
}
