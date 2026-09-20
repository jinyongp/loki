package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestArtifactPublishSchemaRejectsVariantLeaksBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["artifact_publish"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		action := input["action"].(string)
		base := map[string]any{
			"kind":        action,
			"share_id":    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"url":         "https://example.test/artifacts/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"expires_at":  "2026-09-20T00:15:00+00:00",
			"filename":    "fixture.txt",
			"mime_type":   "text/plain",
			"bytes":       5,
			"sha256":      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"disposition": "attachment",
		}
		if action == "file" {
			base["path"] = input["path"]
		} else {
			base["filename"] = "bundle.zip"
			if filename, ok := input["filename"].(string); ok {
				base["filename"] = filename
			}
			base["mime_type"] = "application/zip"
			base["paths"] = input["paths"]
			base["file_count"] = 1
			base["input_bytes"] = 5
			base["excluded_entries"] = 0
		}
		return Object(base)
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{
			"action": "file", "path": "hello.txt",
			"request_id": "79000000-0000-4000-8000-000000000001",
		},
		{
			"action": "file", "path": "hello.txt", "ttl_seconds": 3600,
			"request_id": "79000000-0000-4000-8000-000000000002",
		},
		{
			"action": "bundle", "paths": []any{"hello.txt"},
			"request_id": "79000000-0000-4000-8000-000000000003",
		},
		{
			"action": "bundle", "paths": []any{"hello.txt"}, "filename": "custom.ZIP", "ttl_seconds": 3600,
			"request_id": "79000000-0000-4000-8000-000000000004",
		},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid artifact_publish rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid artifact calls = %d want %d", calls, len(valid))
	}

	invalid := []map[string]any{
		{"action": "file", "path": "hello.txt"},
		{"action": "file", "request_id": "79000000-0000-4000-8000-000000000010"},
		{
			"action": "file", "path": "hello.txt", "filename": "x.zip",
			"request_id": "79000000-0000-4000-8000-000000000011",
		},
		{
			"action": "file", "path": "hello.txt", "paths": []any{"hello.txt"},
			"request_id": "79000000-0000-4000-8000-000000000012",
		},
		{"action": "bundle", "paths": []any{"hello.txt"}},
		{"action": "bundle", "request_id": "79000000-0000-4000-8000-000000000013"},
		{
			"action": "bundle", "paths": []any{"hello.txt"}, "path": "hello.txt",
			"request_id": "79000000-0000-4000-8000-000000000014",
		},
		{
			"action": "bundle", "paths": []any{"hello.txt"}, "filename": "../x.zip",
			"request_id": "79000000-0000-4000-8000-000000000015",
		},
		{
			"action": "bundle", "paths": []any{"hello.txt"}, "filename": "x.txt",
			"request_id": "79000000-0000-4000-8000-000000000016",
		},
		{
			"action": "bundle", "paths": []any{"hello.txt", "hello.txt"},
			"request_id": "79000000-0000-4000-8000-000000000017",
		},
		{
			"action": "bundle", "paths": []any{"hello.txt"}, "ttl_seconds": 1,
			"request_id": "79000000-0000-4000-8000-000000000018",
		},
		{
			"action": "bundle", "paths": []any{"hello.txt"},
			"request_id": "bad",
		},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid artifact_publish accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid artifact calls reached handler: calls=%d want=%d", calls, len(valid))
	}
}
