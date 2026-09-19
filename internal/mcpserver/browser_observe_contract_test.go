package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func observeEventResult(key string) map[string]any {
	return map[string]any{
		key: []any{}, "latest_sequence": 0, "oldest_sequence": 0, "next_sequence": 0,
		"retained": 0, "complete": true, "browser_generation": 1,
	}
}

func TestBrowserObserveSchemaRejectsActionIrrelevantFilters(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["browser_observe"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		action, _ := input["action"].(string)
		if action == "" {
			action = "state"
		}
		switch action {
		case "state":
			return Object(map[string]any{
				"url": "https://example.test", "title": "fixture",
				"interactive_elements": []any{}, "pixels_above": 0, "pixels_below": 0,
				"viewport": map[string]any{
					"width": 1280, "height": 800, "scroll_x": 0, "scroll_y": 0,
					"page_width": 1280, "page_height": 800,
				},
				"tabs": []any{}, "active_tab_id": "abcd",
				"browser_generation": 1, "state_generation": 1,
			})
		case "tabs":
			return Object(map[string]any{
				"active_tab_id": "abcd", "tabs": []any{}, "browser_generation": 1,
			})
		case "console", "websockets":
			return Object(observeEventResult("events"))
		case "errors":
			return Object(observeEventResult("errors"))
		case "network":
			return Object(observeEventResult("requests"))
		case "request":
			return Object(map[string]any{
				"request_id": input["request_id"], "session_id": "session",
				"url": "https://example.test", "method": "GET", "resource_type": "Fetch",
				"request_headers": map[string]any{}, "status": nil, "mime_type": nil,
				"failed": false, "finished": false, "encoded_bytes": nil,
				"browser_generation": 1,
			})
		case "diagnostics":
			return Object(map[string]any{
				"summary": map[string]any{
					"console_events": 0, "page_errors": 0, "network_requests": 0,
					"failed_requests": 0, "websocket_events": 0, "latest_sequence": 0,
				},
				"recent_console": []any{}, "recent_page_errors": []any{},
				"recent_failed_requests": []any{}, "recent_websockets": []any{},
				"page":            map[string]any{"url": "https://example.test", "title": "fixture"},
				"latest_sequence": 0, "oldest_sequence": 0, "next_sequence": 0,
				"complete": true, "browser_generation": 1,
			})
		}
		panic("unexpected browser_observe action")
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{},
		{"action": "tabs"},
		{"action": "console", "since_sequence": 2, "limit": 500, "level": "warning"},
		{"action": "network", "since_sequence": 2, "limit": 500, "status_min": 400, "failed_only": true, "resource_type": "Fetch"},
		{"action": "request", "request_id": "request-1", "include_body": true, "max_body_chars": 1024},
		{"action": "websockets", "since_sequence": 2, "limit": 500},
		{"action": "errors", "since_sequence": 2, "limit": 500},
		{"action": "diagnostics", "since_sequence": 2, "limit": 200},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "browser_observe", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid browser_observe rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid browser_observe calls = %d", calls)
	}

	invalid := []map[string]any{
		{"action": "state", "limit": 1},
		{"action": "tabs", "since_sequence": 1},
		{"action": "console", "status_min": 400},
		{"action": "network", "level": "warning"},
		{"action": "request"},
		{"action": "request", "request_id": "r", "limit": 1},
		{"action": "websockets", "level": "warning"},
		{"action": "errors", "failed_only": true},
		{"action": "diagnostics", "limit": 201},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "browser_observe", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid browser_observe accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid browser_observe reached handler: calls=%d want=%d", calls, len(valid))
	}
}
