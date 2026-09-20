package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func projectContextBasisFixture() map[string]any {
	return map[string]any{
		"repository_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"worktree_id":   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"skills":        []any{}, "validation_record_ids": []any{}, "evidence_refs": []any{}, "gaps": []any{},
	}
}

func projectContextReadFixture() map[string]any {
	basis := projectContextBasisFixture()
	return map[string]any{
		"complete":          true,
		"basis":             basis,
		"basis_fingerprint": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"guidance": map[string]any{
			"target": ".", "target_dir": ".", "revision": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			"complete": true, "sources": []any{}, "diagnostics": []any{}, "total_bytes": 0,
		},
		"skills":          map[string]any{"complete": true, "items": []any{}, "diagnostics": []any{}, "shadowed": []any{}},
		"selected_skills": []any{},
		"coordination": map[string]any{
			"current": map[string]any{
				"profile": "default", "revision": 1, "truncated": false, "reason": "", "omitted_ids": []any{},
			},
			"transition": map[string]any{"action": "none", "reason": "no_current_or_next_task"},
		},
		"transition": map[string]any{"action": "none", "reason": "no_current_or_next_task"},
		"ambiguous":  false,
		"checkpoint": map[string]any{
			"found": false, "stale": false, "stale_reasons": []any{}, "expected_previous": "missing",
		},
	}
}

func projectContextWriteFixture() map[string]any {
	basis := projectContextBasisFixture()
	return map[string]any{
		"basis_fingerprint": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"record": map[string]any{
			"version":           1,
			"id":                "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			"request_id":        "80000000-0000-4000-8000-000000000001",
			"created_at":        "2026-09-20T00:00:00Z",
			"scope_key":         "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			"basis":             basis,
			"basis_fingerprint": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			"draft_fingerprint": "1111111111111111111111111111111111111111111111111111111111111111",
			"summary":           "summary", "decisions": []any{}, "remaining": []any{}, "blockers": []any{},
			"next_action": "next",
		},
		"replayed": false, "pruned_records": 0, "retention_gap": nil,
	}
}

func TestProjectContextSchemasValidateBeforeHandlers(t *testing.T) {
	handlers := testHandlers(t)
	calls := map[string]int{}
	handlers["project_context"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls["read"]++
		return Object(projectContextReadFixture())
	}
	handlers["project_context_write"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls["write"]++
		return Object(projectContextWriteFixture())
	}
	client := connect(t, handlers)

	for _, arguments := range []map[string]any{
		{},
		{"cwd": ".", "target": ".", "skills": []any{}},
	} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "project_context", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid project_context rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	validWrite := map[string]any{
		"request_id":        "80000000-0000-4000-8000-000000000001",
		"expected_basis":    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"expected_previous": "missing",
		"summary":           "summary",
		"next_action":       "next",
	}
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "project_context_write", Arguments: validWrite})
	if err != nil || result.IsError {
		t.Fatalf("valid project_context_write rejected: result=%#v err=%v", result, err)
	}
	if calls["read"] != 2 || calls["write"] != 1 {
		t.Fatalf("valid project context calls = %#v", calls)
	}

	invalid := []struct {
		name string
		args map[string]any
	}{
		{name: "project_context", args: map[string]any{"unknown": true}},
		{name: "project_context", args: map[string]any{"skills": []any{"Bad Skill"}}},
		{name: "project_context_write", args: map[string]any{
			"request_id":        "80000000-0000-4000-8000-000000000002",
			"expected_previous": "missing", "summary": "summary", "next_action": "next",
		}},
		{name: "project_context_write", args: map[string]any{
			"request_id":        "bad",
			"expected_basis":    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			"expected_previous": "missing", "summary": "summary", "next_action": "next",
		}},
		{name: "project_context_write", args: map[string]any{
			"request_id":        "80000000-0000-4000-8000-000000000003",
			"expected_basis":    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			"expected_previous": "bad", "summary": "summary", "next_action": "next",
		}},
		{name: "project_context_write", args: map[string]any{
			"request_id":        "80000000-0000-4000-8000-000000000004",
			"expected_basis":    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			"expected_previous": "missing", "summary": "summary", "next_action": "next", "unknown": true,
		}},
	}
	for _, test := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil || !result.IsError {
			t.Fatalf("invalid %s accepted: args=%#v result=%#v err=%v", test.name, test.args, result, err)
		}
	}
	if calls["read"] != 2 || calls["write"] != 1 {
		t.Fatalf("invalid project context calls reached handlers: %#v", calls)
	}
}
