package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPreviewPublishSchemaRejectsInvalidCreationBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["preview_publish"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		requestID := input["request_id"].(string)
		routes := map[string]any{"/": float64(43000)}
		if supplied, ok := input["routes"].(map[string]any); ok {
			routes = supplied
		}
		return Object(map[string]any{
			"share_id":         "0123456789abcdef",
			"url":              "https://loki-example.preview.test",
			"port":             43000,
			"routes":           routes,
			"cwd":              "/workspace/repo",
			"command":          "node",
			"created_at":       "2026-09-20T00:00:00+00:00",
			"expires_at":       "2026-09-20T00:15:00+00:00",
			"display_markdown": "[Open live preview](https://loki-example.preview.test)",
			"request_id":       requestID,
		})
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{"action": "server", "port": 43000, "request_id": "75000000-0000-4000-8000-000000000001"},
		{
			"action": "stack", "routes": map[string]any{"/": 43000, "/api": 43001},
			"ttl_seconds": 1200, "request_id": "75000000-0000-4000-8000-000000000002",
		},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "preview_publish", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid preview_publish rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid preview calls = %d", calls)
	}

	invalid := []map[string]any{
		{"action": "server", "port": 43000},
		{"action": "server", "port": 43000, "request_id": "invalid"},
		{"action": "server", "port": 43000, "routes": map[string]any{"/": 43000}, "request_id": "76000000-0000-4000-8000-000000000001"},
		{"action": "stack", "request_id": "76000000-0000-4000-8000-000000000002"},
		{"action": "stack", "routes": map[string]any{"/": 43000}, "port": 43000, "request_id": "76000000-0000-4000-8000-000000000003"},
		{"action": "server", "port": 43000, "ttl_seconds": 1, "request_id": "76000000-0000-4000-8000-000000000004"},
		{"action": "server", "port": 43000, "environment_routes": map[string]any{"PUBLIC_API": "x"}, "request_id": "76000000-0000-4000-8000-000000000005"},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "preview_publish", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid preview_publish accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid preview_publish reached handler: calls=%d want=%d", calls, len(valid))
	}
}
