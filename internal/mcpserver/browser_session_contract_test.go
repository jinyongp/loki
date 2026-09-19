package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBrowserSessionSchemaRejectsIrrelevantFieldsBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["browser_session"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		switch input["action"] {
		case "start":
			return Object(map[string]any{"status": "running", "active_tab_id": "abcd", "browser_generation": 1})
		case "navigate":
			return Object(map[string]any{
				"url": input["url"], "title": "fixture", "new_tab": input["new_tab"] == true,
				"active_tab_id": "abcd", "browser_generation": 2,
			})
		case "stop":
			return Object(map[string]any{"status": "stopped", "browser_generation": 3})
		}
		panic("unexpected browser_session action")
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{"action": "start"},
		{"action": "navigate", "url": "https://example.com"},
		{"action": "navigate", "url": "https://example.com", "new_tab": true},
		{"action": "stop"},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "browser_session", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid browser_session rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid browser_session calls = %d", calls)
	}

	invalid := []map[string]any{
		{},
		{"action": "start", "url": "https://example.com"},
		{"action": "start", "new_tab": true},
		{"action": "navigate"},
		{"action": "stop", "url": "https://example.com"},
		{"action": "stop", "new_tab": true},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "browser_session", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid browser_session accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid browser_session reached handler: calls=%d want=%d", calls, len(valid))
	}
}
