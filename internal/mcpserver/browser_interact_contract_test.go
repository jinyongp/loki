package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBrowserInteractSchemaRejectsInvalidActionShapesBeforeHandler(t *testing.T) {
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
			button := "left"
			if value, ok := input["button"].(string); ok {
				button = value
			}
			clickCount := 1
			if value, ok := input["click_count"].(float64); ok {
				clickCount = int(value)
			}
			modifiers := []any{}
			if value, ok := input["modifiers"].([]any); ok {
				modifiers = value
			}
			return Object(map[string]any{
				"clicked": clicked, "button": button, "click_count": clickCount,
				"modifiers": modifiers, "new_tab": input["new_tab"] == true, "browser_generation": 8,
			})
		case "hover":
			hovered := map[string]any{"x": 10, "y": 20}
			if index, ok := input["index"]; ok {
				hovered = map[string]any{"index": index}
			}
			return Object(map[string]any{"hovered": hovered, "browser_generation": 8})
		case "drag":
			dragged := map[string]any{
				"from_x": 10, "from_y": 20, "to_x": 30, "to_y": 40, "steps": 12, "duration_ms": 250,
			}
			if index, ok := input["source_index"]; ok {
				dragged["source_index"] = index
			}
			if index, ok := input["target_index"]; ok {
				dragged["target_index"] = index
			}
			return Object(map[string]any{"dragged": dragged, "browser_generation": 8})
		case "wheel":
			wheel := map[string]any{"x": 640, "y": 400, "delta_x": input["delta_x"], "delta_y": input["delta_y"]}
			if index, ok := input["index"]; ok {
				wheel["index"] = index
			}
			return Object(map[string]any{"wheel": wheel, "browser_generation": 8})
		case "fill":
			return Object(map[string]any{"filled": true, "index": input["index"], "characters": 4, "browser_generation": 8})
		case "type":
			return Object(map[string]any{"typed": true, "index": input["index"], "characters": 4, "browser_generation": 8})
		case "key":
			modifiers := []any{}
			if value, ok := input["modifiers"].([]any); ok {
				modifiers = value
			}
			return Object(map[string]any{"key": input["key"], "modifiers": modifiers, "browser_generation": 8})
		case "shortcut":
			return Object(map[string]any{"shortcut": map[string]any{"key": input["key"], "modifiers": input["modifiers"]}, "browser_generation": 8})
		case "select_option":
			return Object(map[string]any{
				"index": input["index"], "multiple": false, "changed": true,
				"selected":           []any{map[string]any{"index": 0, "value": "one", "label": "One"}},
				"browser_generation": 8,
			})
		case "set_checked":
			return Object(map[string]any{"index": input["index"], "checked": input["checked"], "changed": true, "browser_generation": 8})
		case "focus":
			return Object(map[string]any{"focused": true, "index": input["index"], "browser_generation": 8})
		case "upload":
			return Object(map[string]any{
				"uploaded": true, "index": input["index"],
				"files":       []any{map[string]any{"path": "fixtures/input.txt", "name": "input.txt", "bytes": 4}},
				"total_bytes": 4, "browser_generation": 8,
			})
		case "dialog":
			return Object(map[string]any{
				"dialog_handled": true, "accepted": input["accept"], "type": "confirm",
				"dialog_generation": 4, "browser_generation": 8,
			})
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
		{"action": "click", "index": 1, "button": "right", "click_count": 2, "modifiers": []any{"Control", "Shift"}, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "click", "index": 1, "new_tab": true, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "click", "x": 10, "y": 20, "button": "middle", "expected_browser_generation": 7},
		{"action": "hover", "index": 1, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "hover", "x": 10, "y": 20, "expected_browser_generation": 7},
		{"action": "drag", "source_index": 1, "target_index": 2, "steps": 8, "duration_ms": 0, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "drag", "source_index": 1, "to_x": 30, "to_y": 40, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "drag", "from_x": 10, "from_y": 20, "to_x": 30, "to_y": 40, "expected_browser_generation": 7},
		{"action": "wheel", "delta_x": 0, "delta_y": 500, "expected_browser_generation": 7},
		{"action": "wheel", "index": 1, "delta_x": 0, "delta_y": 500, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "wheel", "x": 10, "y": 20, "delta_x": 100, "delta_y": 0, "expected_browser_generation": 7},
		{"action": "fill", "index": 1, "text": "test", "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "type", "index": 1, "text": "test", "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "key", "key": "Enter", "expected_browser_generation": 7},
		{"action": "key", "key": "A", "modifiers": []any{"Shift"}, "expected_browser_generation": 7},
		{"action": "shortcut", "key": "K", "modifiers": []any{"Control"}, "expected_browser_generation": 7},
		{"action": "select_option", "index": 1, "options": []any{map[string]any{"value": "one"}}, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "set_checked", "index": 1, "checked": true, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "focus", "index": 1, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "upload", "index": 1, "paths": []any{"fixtures/input.txt"}, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "dialog", "accept": true, "expected_browser_generation": 7, "expected_dialog_generation": 3},
		{"action": "dialog", "accept": true, "prompt_text": "answer", "expected_browser_generation": 7, "expected_dialog_generation": 3},
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
		{"action": "scroll", "expected_browser_generation": 7},
		{"action": "press", "key": "Enter", "expected_browser_generation": 7},
		{"action": "click", "index": 1, "expected_browser_generation": 7},
		{"action": "click", "index": 1, "new_tab": true, "button": "right", "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "click", "x": 10, "y": 20, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "hover", "index": 1, "expected_browser_generation": 7},
		{"action": "hover", "index": 1, "x": 10, "y": 20, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "drag", "source_index": 1, "target_index": 2, "expected_browser_generation": 7},
		{"action": "drag", "from_x": 10, "from_y": 20, "target_index": 2, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "drag", "from_x": 10, "from_y": 20, "to_x": 30, "expected_browser_generation": 7},
		{"action": "wheel", "index": 1, "delta_x": 0, "delta_y": 500, "expected_browser_generation": 7},
		{"action": "wheel", "x": 10, "y": 20, "delta_x": 0, "delta_y": 500, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "wheel", "delta_y": 500, "expected_browser_generation": 7},
		{"action": "fill", "index": 1, "text": "test", "expected_browser_generation": 7},
		{"action": "type", "index": 1, "text": "test", "expected_browser_generation": 7},
		{"action": "key", "key": "Enter", "index": 1, "expected_browser_generation": 7},
		{"action": "shortcut", "key": "K", "modifiers": []any{}, "expected_browser_generation": 7},
		{"action": "select_option", "index": 1, "options": []any{}, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "select_option", "index": 1, "options": []any{map[string]any{"value": "x", "label": "X"}}, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "set_checked", "index": 1, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "focus", "index": 1, "text": "x", "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "upload", "index": 1, "paths": []any{"fixtures/input.txt"}, "expected_browser_generation": 7},
		{"action": "upload", "index": 1, "paths": []any{}, "expected_browser_generation": 7, "expected_state_generation": 3},
		{"action": "dialog", "accept": true, "expected_browser_generation": 7},
		{"action": "dialog", "accept": true, "expected_dialog_generation": 3},
		{"action": "dialog", "expected_browser_generation": 7, "expected_dialog_generation": 3},
		{"action": "dialog", "accept": false, "prompt_text": "ignored", "expected_browser_generation": 7, "expected_dialog_generation": 3},
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
