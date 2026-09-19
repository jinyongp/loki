package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBrowserInteractSchemaRejectsStaleShapeBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["browser_interact"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		action := input["action"].(string)
		switch action {
		case "click":
			clicked := map[string]any{"x": 10, "y": 20}
			if index, ok := input["index"]; ok {
				clicked = map[string]any{"index": index}
			}
			return Object(map[string]any{"clicked": clicked, "new_tab": input["new_tab"] == true, "browser_generation": 8})
		case "type":
			return Object(map[string]any{"typed": true, "index": input["index"], "characters": 4, "browser_generation": 8})
		case "press":
			return Object(map[string]any{"pressed": input["key"], "browser_generation": 8})
		case "scroll":
			return Object(map[string]any{"direction": "down", "amount": 500, "browser_generation": 8})
		case "back":
			return Object(map[string]any{"url": "https://example.com", "title": "fixture", "browser_generation": 8})
		case "switch_tab":
			return Object(map[string]any{"url": "https://example.com", "title": "fixture", "tab_id": input["tab_id"], "browser_generation": 8})
		case "close_tab":
			return Object(map[string]any{"closed": input["tab_id"], "active_tab_id": nil, "browser_generation": 8})
		default:
			return nil, nil
		}
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{"action": "click", "index": 1, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "click", "x": 10, "y": 20, "expected_browser_generation": 7},
		{"action": "type", "index": 1, "text": "test", "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "press", "key": "Enter", "expected_browser_generation": 7},
		{"action": "scroll", "expected_browser_generation": 7},
		{"action": "back", "expected_browser_generation": 7},
		{"action": "switch_tab", "tab_id": "abcd", "expected_browser_generation": 7},
		{"action": "close_tab", "tab_id": "abcd", "expected_browser_generation": 7},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "browser_interact", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid browser_interact rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid browser interaction calls = %d, want %d", calls, len(valid))
	}

	invalid := []map[string]any{
		{"action": "click", "index": 1, "expected_browser_generation": 7},
		{"action": "click", "x": 10, "y": 20, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "click", "index": 1, "x": 10, "y": 20, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "type", "index": 1, "text": "test", "expected_browser_generation": 7},
		{"action": "press", "key": "Enter"},
		{"action": "press", "key": "Enter", "index": 1, "expected_browser_generation": 7},
		{"action": "scroll", "tab_id": "abcd", "expected_browser_generation": 7},
		{"action": "switch_tab", "expected_browser_generation": 7},
		{"action": "close_tab", "tab_id": "abcd"},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "browser_interact", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid browser_interact accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid browser_interact reached handler: calls=%d want=%d", calls, len(valid))
	}
}
