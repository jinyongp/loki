package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/artifacts"
	"loki/internal/config"
	"loki/internal/contract"
	"loki/internal/mcpserver"
	"loki/internal/workspace"
)

func TestBrowserMCPAndScreenshotHistory(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	files, err := workspace.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	var content bytes.Buffer
	if err = png.Encode(&content, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	data := content.Bytes()
	captureCount := 0
	operations := []string{}
	browser := browserFixture(func(_ context.Context, operation string, args map[string]any) (map[string]any, error) {
		operations = append(operations, operation)
		sequenceResult := func(key string) map[string]any {
			return map[string]any{
				key: []any{}, "latest_sequence": 0, "oldest_sequence": 0, "next_sequence": 0,
				"retained": 0, "complete": true, "browser_generation": 3,
			}
		}
		switch operation {
		case "screenshot":
			captureCount++
			return map[string]any{"data_base64": base64.StdEncoding.EncodeToString(data)}, nil
		case "start":
			return map[string]any{"status": "running", "active_tab_id": "abcd", "browser_generation": 1}, nil
		case "navigate":
			return map[string]any{
				"url": args["url"], "title": "fixture", "new_tab": args["new_tab"] == true,
				"active_tab_id": "abcd", "browser_generation": 2,
			}, nil
		case "stop":
			return map[string]any{"status": "stopped", "browser_generation": 3}, nil
		case "state":
			return map[string]any{
				"url": "https://example.com", "title": "fixture",
				"interactive_elements": []any{}, "pixels_above": 0, "pixels_below": 0,
				"viewport": map[string]any{
					"width": 1280, "height": 800, "scroll_x": 0, "scroll_y": 0,
					"page_width": 1280, "page_height": 800,
				},
				"tabs": []any{}, "active_tab_id": "abcd",
				"browser_generation": 3, "state_generation": 1,
			}, nil
		case "list_tabs":
			return map[string]any{"active_tab_id": "abcd", "tabs": []any{}, "browser_generation": 3}, nil
		case "console", "websockets":
			return sequenceResult("events"), nil
		case "page_errors":
			return sequenceResult("errors"), nil
		case "network":
			return sequenceResult("requests"), nil
		case "request":
			return map[string]any{
				"request_id": args["request_id"], "session_id": "session",
				"url": "https://example.com", "method": "GET", "resource_type": "Fetch",
				"request_headers": map[string]any{}, "status": nil, "mime_type": nil,
				"failed": false, "finished": false, "encoded_bytes": nil,
				"browser_generation": 3,
			}, nil
		case "debug_diagnostics":
			return map[string]any{
				"summary": map[string]any{
					"console_events": 0, "page_errors": 0, "network_requests": 0,
					"failed_requests": 0, "websocket_events": 0, "latest_sequence": 0,
				},
				"recent_console": []any{}, "recent_page_errors": []any{},
				"recent_failed_requests": []any{}, "recent_websockets": []any{},
				"page":            map[string]any{"url": "https://example.com", "title": "fixture"},
				"latest_sequence": 0, "oldest_sequence": 0, "next_sequence": 0,
				"complete": true, "browser_generation": 3,
			}, nil
		case "click":
			return map[string]any{"clicked": map[string]any{"index": 0}, "new_tab": false, "browser_generation": 4}, nil
		case "type":
			return map[string]any{"typed": true, "index": 0, "characters": 4, "browser_generation": 4}, nil
		case "press":
			return map[string]any{"pressed": args["key"], "browser_generation": 4}, nil
		case "scroll":
			return map[string]any{"direction": "down", "amount": 500, "browser_generation": 4}, nil
		case "back":
			return map[string]any{"url": "https://example.com", "title": "fixture", "browser_generation": 4}, nil
		case "switch_tab":
			return map[string]any{"url": "https://example.com", "title": "fixture", "tab_id": args["tab_id"], "browser_generation": 4}, nil
		case "close_tab":
			return map[string]any{"closed": args["tab_id"], "active_tab_id": "abcd", "browser_generation": 4}, nil
		default:
			return map[string]any{"operation": operation, "arguments": args}, nil
		}
	})
	store := artifacts.New(artifacts.Options{BaseURL: "https://example.test/artifacts", AllowedHosts: []string{"example.test"}})
	handlers := BrowserHandlers(browser, files, store)
	definitions, _ := contract.CurrentDefinitions()
	for _, definition := range definitions {
		if handlers[definition.Name] == nil {
			handlers[definition.Name] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return nil, errors.New("outside browser fixture")
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
	client, err := mcp.NewClient(&mcp.Implementation{Name: "browser-test", Version: "1"}, nil).Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		if args == nil {
			args = map[string]any{}
		}
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s: %v %+v", name, err, result)
		}
		return result
	}
	decode := func(r *mcp.CallToolResult) map[string]any {
		t.Helper()
		data, _ := json.Marshal(r.StructuredContent)
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, test := range []struct {
		action string
		args   map[string]any
	}{
		{action: "start", args: map[string]any{"action": "start"}},
		{action: "navigate", args: map[string]any{"action": "navigate", "url": "https://example.com", "new_tab": true}},
		{action: "stop", args: map[string]any{"action": "stop"}},
	} {
		result := decode(call("browser_session", test.args))
		if result["browser_generation"] == nil {
			t.Fatalf("browser_session %s omitted generation: %#v", test.action, result)
		}
	}
	for tool, actions := range map[string][]string{"browser_observe": {"state", "tabs", "console", "network", "request", "websockets", "errors", "diagnostics"}, "browser_interact": {"click", "type", "press", "scroll", "back", "switch_tab", "close_tab"}} {
		for _, action := range actions {
			args := map[string]any{"action": action}
			switch tool {
			case "browser_observe":
				switch action {
				case "console":
					args["level"] = "warning"
				case "network":
					args["failed_only"] = true
				case "request":
					args["request_id"] = "r1"
				case "diagnostics":
					args["limit"] = 50
				}
			case "browser_interact":
				args["expected_browser_generation"] = 3
				switch action {
				case "click":
					args["index"], args["expected_state_generation"] = 0, 1
				case "type":
					args["index"], args["text"], args["expected_state_generation"] = 0, "test", 1
				case "press":
					args["key"] = "Enter"
				case "scroll":
					args["direction"], args["amount"] = "down", 500
				case "switch_tab", "close_tab":
					args["tab_id"] = "1234"
				}
			}
			result := decode(call(tool, args))
			want := action
			if action == "tabs" {
				want = "list_tabs"
			}
			if action == "errors" {
				want = "page_errors"
			}
			if action == "diagnostics" {
				want = "debug_diagnostics"
			}
			if operations[len(operations)-1] != want {
				t.Fatalf("%s %s operation = %q, want %q", tool, action, operations[len(operations)-1], want)
			}
			if tool == "browser_observe" {
				if result["browser_generation"] == nil {
					t.Fatalf("%s omitted browser_generation: %#v", action, result)
				}
			} else if result["browser_generation"] == nil {
				t.Fatalf("%s omitted browser_generation: %#v", action, result)
			}
		}
	}
	defaultState := decode(call("browser_observe", nil))
	if operations[len(operations)-1] != "state" || defaultState["state_generation"] == nil {
		t.Fatalf("default browser observe = %#v operation=%q", defaultState, operations[len(operations)-1])
	}
	shot := call("browser_screenshot", nil)
	if image, ok := shot.Content[0].(*mcp.ImageContent); !ok || !bytes.Equal(image.Data, data) || image.MIMEType != "image/png" {
		t.Fatal(shot)
	}
	saved := decode(call("browser_save_screenshot", map[string]any{"path": "shots/current.png", "full_page": true}))
	if saved["full_page"] != true || saved["sha256"] != workspace.Digest(data) || saved["previous_revision"] != nil {
		t.Fatal(saved)
	}
	if disk, err := os.ReadFile(filepath.Join(c.Root, "shots/current.png")); err != nil || !bytes.Equal(disk, data) {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{"path": "shots/current.png"}, {"path": "shots/current.png", "overwrite": true, "expected_sha256": "wrong"}, {"path": "../outside.png"}, {"path": "shots/current.jpg"}} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "browser_save_screenshot", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatal(args, err, result)
		}
	}
	saved = decode(call("browser_save_screenshot", map[string]any{"path": "shots/current.png", "overwrite": true, "expected_sha256": workspace.Digest(data)}))
	if saved["previous_revision"] == nil {
		t.Fatal("overwrite lost history")
	}
	history, err := files.Revisions("shots/current.png", 10)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(history)
	if !bytes.Contains(encoded, []byte("browser_save_screenshot")) {
		t.Fatal(string(encoded))
	}
	shared := decode(call("browser_share_screenshot", map[string]any{"full_page": true}))
	w := httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequest("GET", shared["url"].(string), nil))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) || shared["path"] != "browser://active-tab" || shared["full_page"] != true {
		t.Fatal(w.Code, shared)
	}
	before := captureCount
	if result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "browser_share_screenshot", Arguments: map[string]any{"ttl_seconds": 1}}); err != nil || !result.IsError || captureCount != before {
		t.Fatal("invalid TTL captured screenshot", err)
	}
}
