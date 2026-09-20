package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSecretMutationSchemasRejectUnsafeShapesBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	writeCalls, deleteCalls := 0, 0
	handlers["secret_write"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		writeCalls++
		action := input["action"].(string)
		base := map[string]any{"profile": input["profile"], "revision": 2}
		switch action {
		case "create_profile":
			base["created"], base["request_id"] = true, input["request_id"]
		case "import_staged":
			base["imported"] = []any{"APP_MODE"}
			base["count"], base["import_id"], base["request_id"] = 1, input["import_id"], input["request_id"]
		case "set_public":
			base["name"], base["stored"], base["request_id"] = input["name"], true, input["request_id"]
		case "generate":
			base["secret"], base["generated"], base["bytes"], base["request_id"] = input["secret"], true, 32, input["request_id"]
		}
		return Object(base)
	}
	handlers["secret_delete"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		deleteCalls++
		base := map[string]any{
			"profile": input["profile"], "removed": true, "revision": 3, "request_id": input["request_id"],
		}
		if input["action"] == "secret" {
			base["secret"] = input["secret"]
		}
		return Object(base)
	}
	client := connect(t, handlers)

	validWrite := []map[string]any{
		{"action": "create_profile", "profile": "web", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000001"},
		{"action": "import_staged", "profile": "web", "expected_revision": 2, "request_id": "88000000-0000-4000-8000-000000000007", "import_id": "0123456789abcdef0123456789abcdef"},
		{"action": "set_public", "profile": "web", "expected_revision": 2, "request_id": "88000000-0000-4000-8000-000000000002", "name": "APP_MODE", "value": "local"},
		{"action": "generate", "profile": "web", "expected_revision": 2, "request_id": "88000000-0000-4000-8000-000000000003", "secret": "SESSION_KEY"},
		{"action": "generate", "profile": "web", "expected_revision": 2, "request_id": "88000000-0000-4000-8000-000000000004", "secret": "SESSION_KEY", "bytes": 128},
	}
	for _, arguments := range validWrite {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "secret_write", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid secret_write rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	validDelete := []map[string]any{
		{"action": "profile", "profile": "web", "expected_revision": 2, "request_id": "88000000-0000-4000-8000-000000000005"},
		{"action": "secret", "profile": "web", "secret": "SESSION_KEY", "expected_revision": 2, "request_id": "88000000-0000-4000-8000-000000000006"},
	}
	for _, arguments := range validDelete {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "secret_delete", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid secret_delete rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	beforeWrite, beforeDelete := writeCalls, deleteCalls

	invalidWrite := []map[string]any{
		{"action": "set", "profile": "web", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000010", "secret": "APP_MODE", "value": "local"},
		{"action": "import_env", "profile": "web", "expected_revision": 1, "import_id": "0123456789abcdef0123456789abcdef"},
		{"action": "create_profile", "profile": "web", "request_id": "88000000-0000-4000-8000-000000000011"},
		{"action": "create_profile", "profile": "web", "expected_revision": 1},
		{"action": "create_profile", "profile": "web", "expected_revision": 1, "request_id": "bad"},
		{"action": "create_profile", "profile": "web", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000012", "secret": "X"},
		{"action": "import_staged", "profile": "web", "expected_revision": 1, "import_id": "0123456789abcdef0123456789abcdef"},
		{"action": "import_staged", "profile": "web", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000013"},
		{"action": "set_public", "profile": "web", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000014", "name": "APP_MODE"},
		{"action": "set_public", "profile": "web", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000017", "secret": "APP_MODE", "value": "local"},
		{"action": "generate", "profile": "web", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000015", "secret": "SESSION_KEY", "value": "x"},
		{"action": "generate", "profile": "web", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000016", "secret": "SESSION_KEY", "bytes": 129},
	}
	for _, arguments := range invalidWrite {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "secret_write", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid secret_write accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	invalidDelete := []map[string]any{
		{"action": "profile", "profile": "web", "request_id": "88000000-0000-4000-8000-000000000020"},
		{"action": "profile", "profile": "web", "expected_revision": 1},
		{"action": "profile", "profile": "web", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000021", "secret": "X"},
		{"action": "secret", "profile": "web", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000022"},
		{"action": "secret", "profile": "Bad Name", "secret": "X", "expected_revision": 1, "request_id": "88000000-0000-4000-8000-000000000023"},
	}
	for _, arguments := range invalidDelete {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "secret_delete", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid secret_delete accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if writeCalls != beforeWrite || deleteCalls != beforeDelete {
		t.Fatalf("invalid secret mutation reached handlers: write=%d/%d delete=%d/%d", writeCalls, beforeWrite, deleteCalls, beforeDelete)
	}
}
