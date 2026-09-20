package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestImageSchemasRejectInvalidCallsBeforeHandlers(t *testing.T) {
	handlers := testHandlers(t)
	calls := map[string]int{}
	handlers["read_image"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls["read"]++
		return Object(map[string]any{
			"path": input["path"], "mime_type": "image/png", "bytes": 128,
			"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		})
	}
	handlers["share_image"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls["share"]++
		return Object(map[string]any{
			"path": input["path"], "mime_type": "image/png", "bytes": 128,
			"sha256":           "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"share_id":         "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"url":              "https://example.test/artifacts/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"expires_at":       "2026-09-20T00:15:00+00:00",
			"display_markdown": "![Loki image](https://example.test/image)",
		})
	}
	handlers["write_image"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls["write"]++
		return Object(map[string]any{
			"path": input["path"], "mime_type": input["mime_type"], "bytes": 128,
			"sha256":            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"previous_revision": nil,
		})
	}
	client := connect(t, handlers)

	valid := []struct {
		name string
		args map[string]any
	}{
		{name: "read_image", args: map[string]any{"path": "images/a.png"}},
		{name: "share_image", args: map[string]any{
			"path": "images/a.png", "request_id": "78000000-0000-4000-8000-000000000001",
		}},
		{name: "share_image", args: map[string]any{
			"path": "images/a.png", "request_id": "78000000-0000-4000-8000-000000000002", "ttl_seconds": 3600,
		}},
		{name: "write_image", args: map[string]any{
			"path": "images/a.png", "data_base64": "iVBORw0KGgo=", "mime_type": "image/png",
		}},
		{name: "write_image", args: map[string]any{
			"path": "images/a.png", "data_base64": "iVBORw0KGgo=", "mime_type": "image/png",
			"overwrite": true, "expected_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
	}
	for _, test := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil || result.IsError {
			t.Fatalf("valid %s rejected: args=%#v result=%#v err=%v", test.name, test.args, result, err)
		}
	}
	before := map[string]int{"read": calls["read"], "share": calls["share"], "write": calls["write"]}
	invalid := []struct {
		name string
		args map[string]any
	}{
		{name: "read_image", args: map[string]any{}},
		{name: "read_image", args: map[string]any{"path": "a.png", "ttl_seconds": 900}},
		{name: "share_image", args: map[string]any{"path": "a.png"}},
		{name: "share_image", args: map[string]any{"path": "a.png", "request_id": "bad"}},
		{name: "share_image", args: map[string]any{"path": "a.png", "request_id": "78000000-0000-4000-8000-000000000003", "ttl_seconds": 1}},
		{name: "write_image", args: map[string]any{
			"path": "a.png", "data_base64": "x", "mime_type": "image/png",
			"expected_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		{name: "write_image", args: map[string]any{
			"path": "a.png", "data_base64": "x", "mime_type": "image/png", "overwrite": true,
		}},
		{name: "write_image", args: map[string]any{
			"path": "a.png", "data_base64": "x", "mime_type": "image/png", "overwrite": true, "expected_sha256": "bad",
		}},
		{name: "write_image", args: map[string]any{
			"path": "a.png", "data_base64": "x", "mime_type": "text/plain",
		}},
	}
	for _, test := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil || !result.IsError {
			t.Fatalf("invalid %s accepted: args=%#v result=%#v err=%v", test.name, test.args, result, err)
		}
	}
	for key, count := range before {
		if calls[key] != count {
			t.Fatalf("invalid image calls reached %s handler: got=%d want=%d", key, calls[key], count)
		}
	}
}
