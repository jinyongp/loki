package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSharedResourceAndRevokeSchemasValidateBeforeHandlers(t *testing.T) {
	handlers := testHandlers(t)
	calls := map[string]int{}
	handlers["shared_resources"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls["shared"]++
		return Object(map[string]any{
			"previews":  map[string]any{"previews": []any{}, "configured": false, "complete": true},
			"artifacts": map[string]any{"artifacts": []any{}, "configured": false, "complete": true},
		})
	}
	handlers["revoke_share"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls["revoke"]++
		return Object(map[string]any{
			"kind": input["kind"], "share_id": input["share_id"], "revoked": true,
		})
	}
	client := connect(t, handlers)

	for _, arguments := range []map[string]any{
		{},
		{"kind": "all"},
		{"kind": "previews"},
		{"kind": "artifacts"},
	} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "shared_resources", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid shared_resources rejected: %#v %v", result, err)
		}
	}
	if calls["shared"] != 4 {
		t.Fatalf("shared_resources calls = %#v", calls)
	}
	for _, arguments := range []map[string]any{
		{"kind": "preview", "share_id": strings.Repeat("a", 16)},
		{"kind": "artifact", "share_id": strings.Repeat("A", 43)},
	} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "revoke_share", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid revoke_share rejected: %#v %v", result, err)
		}
	}
	if calls["revoke"] != 2 {
		t.Fatalf("revoke calls = %#v", calls)
	}

	invalid := []struct {
		tool string
		args map[string]any
	}{
		{tool: "shared_resources", args: map[string]any{"kind": "preview"}},
		{tool: "revoke_share", args: map[string]any{"kind": "preview", "share_id": strings.Repeat("A", 43)}},
		{tool: "revoke_share", args: map[string]any{"kind": "artifact", "share_id": strings.Repeat("a", 16)}},
		{tool: "revoke_share", args: map[string]any{"kind": "preview", "share_id": "invalid"}},
	}
	for _, test := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.tool, Arguments: test.args})
		if err != nil || !result.IsError {
			t.Fatalf("invalid %s accepted: args=%#v result=%#v err=%v", test.tool, test.args, result, err)
		}
	}
	if calls["shared"] != 4 || calls["revoke"] != 2 {
		t.Fatalf("invalid share calls reached handlers: %#v", calls)
	}
}
