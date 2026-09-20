package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSecretInspectSchemaRejectsIrrelevantFieldsBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["secret_inspect"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		switch input["action"] {
		case "profiles":
			return Object(map[string]any{
				"profiles": []any{}, "revision": 3, "offset": 0, "limit": 50,
				"has_more": false, "next_offset": nil, "total": 0, "complete": true,
			})
		case "imports":
			return Object(map[string]any{
				"imports": []any{}, "offset": 0, "limit": 50,
				"has_more": false, "next_offset": nil, "total": 0, "complete": true,
			})
		case "profile":
			return Object(map[string]any{
				"name": input["profile"], "secret_names": []any{"TOKEN"},
				"secret_count": 1, "configured_secret_count": 1, "empty_secret_names": []any{},
				"revision": 3,
			})
		case "status":
			return Object(map[string]any{
				"initialized": true, "profiles": 1, "revision": 3,
				"policy_generation": map[string]any{
					"schema": 1, "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
				"devtools": map[string]any{
					"version": "", "commit": "", "protocol_version": 0,
					"approved_commands": 0, "catalog_sha256": "",
				},
				"github": map[string]any{
					"configured": false, "installation_count": 0, "target_count": 0,
					"credential_source": "disabled", "credential_available": false,
				},
			})
		case "audit":
			return Object(map[string]any{
				"records": []any{}, "offset": 0, "limit": 50,
				"has_more": false, "next_offset": nil, "total": 0, "complete": true,
			})
		default:
			return nil, nil
		}
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{"action": "profiles"},
		{"action": "profiles", "offset": 2, "limit": 10},
		{"action": "imports", "limit": 128},
		{"action": "profile", "profile": "web"},
		{"action": "status"},
		{"action": "audit", "offset": 1, "limit": 25},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "secret_inspect", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid secret_inspect rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid secret inspect calls = %d, want %d", calls, len(valid))
	}

	invalid := []map[string]any{
		{},
		{"action": "profiles", "profile": "web"},
		{"action": "imports", "profile": "web"},
		{"action": "profile"},
		{"action": "profile", "profile": "web", "limit": 1},
		{"action": "status", "limit": 1},
		{"action": "audit", "profile": "web"},
		{"action": "audit", "limit": 129},
		{"action": "profiles", "offset": -1},
		{"action": "profile", "profile": "Bad Name"},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "secret_inspect", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid secret_inspect accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid secret inspect calls reached handler: calls=%d want=%d", calls, len(valid))
	}
}
