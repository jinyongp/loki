package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func screenshotMetadata(full bool) map[string]any {
	return map[string]any{
		"path": "browser://active-tab", "mime_type": "image/png", "bytes": 128,
		"sha256":    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"full_page": full,
	}
}

func TestBrowserScreenshotSchemasRejectInvalidCallsBeforeHandlers(t *testing.T) {
	handlers := testHandlers(t)
	calls := map[string]int{}
	handlers["browser_screenshot"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls["screenshot"]++
		return Object(screenshotMetadata(input["full_page"] == true))
	}
	handlers["browser_save_screenshot"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls["save"]++
		return Object(map[string]any{
			"path": input["path"], "bytes": 128, "mime_type": "image/png",
			"sha256":            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"previous_revision": nil, "full_page": input["full_page"] == true,
		})
	}
	handlers["browser_share_screenshot"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls["share"]++
		result := screenshotMetadata(input["full_page"] == true)
		result["share_id"] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		result["url"] = "https://example.test/artifacts/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		result["expires_at"] = "2026-09-20T00:15:00+00:00"
		result["display_markdown"] = "![Loki image](https://example.test/image)"
		return Object(result)
	}
	client := connect(t, handlers)

	valid := []struct {
		name string
		args map[string]any
	}{
		{name: "browser_screenshot", args: map[string]any{}},
		{name: "browser_screenshot", args: map[string]any{"full_page": true}},
		{name: "browser_save_screenshot", args: map[string]any{"path": "shots/current.png"}},
		{name: "browser_save_screenshot", args: map[string]any{
			"path": "shots/current.png", "overwrite": true,
			"expected_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		{name: "browser_share_screenshot", args: map[string]any{
			"request_id": "70000000-0000-4000-8000-000000000001",
		}},
		{name: "browser_share_screenshot", args: map[string]any{
			"request_id": "70000000-0000-4000-8000-000000000002",
			"full_page":  true, "ttl_seconds": 3600,
		}},
	}
	for _, test := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil || result.IsError {
			t.Fatalf("valid %s rejected: args=%#v result=%#v err=%v", test.name, test.args, result, err)
		}
	}

	invalid := []struct {
		name string
		args map[string]any
	}{
		{name: "browser_screenshot", args: map[string]any{"ttl_seconds": 900}},
		{name: "browser_save_screenshot", args: map[string]any{"path": "shots/current.png", "expected_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
		{name: "browser_save_screenshot", args: map[string]any{"path": "shots/current.png", "overwrite": true}},
		{name: "browser_save_screenshot", args: map[string]any{"path": "shots/current.png", "overwrite": true, "expected_sha256": "bad"}},
		{name: "browser_share_screenshot", args: map[string]any{}},
		{name: "browser_share_screenshot", args: map[string]any{"request_id": "bad"}},
		{name: "browser_share_screenshot", args: map[string]any{"request_id": "70000000-0000-4000-8000-000000000003", "ttl_seconds": 1}},
		{name: "browser_share_screenshot", args: map[string]any{"request_id": "70000000-0000-4000-8000-000000000004", "ttl_seconds": 3601}},
	}
	before := map[string]int{"screenshot": calls["screenshot"], "save": calls["save"], "share": calls["share"]}
	for _, test := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil || !result.IsError {
			t.Fatalf("invalid %s accepted: args=%#v result=%#v err=%v", test.name, test.args, result, err)
		}
	}
	for key, count := range before {
		if calls[key] != count {
			t.Fatalf("invalid screenshot calls reached %s handler: got=%d want=%d", key, calls[key], count)
		}
	}
}
