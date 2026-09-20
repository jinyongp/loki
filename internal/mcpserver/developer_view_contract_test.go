package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDeveloperViewSchemaRejectsInspectionAndVariantLeaks(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["developer_view"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		switch input["action"] {
		case "git_diff":
			return Object(map[string]any{
				"kind": "diff", "title": "Worktree changes", "subtitle": input["cwd"],
				"content": input["content"], "truncated": input["truncated"],
				"stats": map[string]any{"files": 1, "additions": 2, "deletions": 0},
			})
		case "test_report":
			return Object(map[string]any{
				"kind": "test", "title": "Test report", "subtitle": input["report_path"],
				"content": "<testsuite/>", "truncated": false,
				"stats": map[string]any{"format": "junit", "tests": 1, "failures": 0, "errors": 0, "skipped": 0},
			})
		default:
			return nil, nil
		}
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{"action": "git_diff", "content": "", "truncated": false},
		{"action": "git_diff", "cwd": "repo", "path": "a.txt", "staged": true, "content": "diff --git a/a b/a\n", "truncated": true},
		{"action": "test_report", "report_path": "reports/junit.xml"},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "developer_view", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid developer_view rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid developer_view calls = %d want %d", calls, len(valid))
	}

	invalid := []map[string]any{
		{"action": "git_diff"},
		{"action": "git_diff", "cwd": "repo", "staged": true},
		{"action": "git_diff", "content": "x", "truncated": false, "report_path": "report.xml"},
		{"action": "test_report", "path": "report.xml"},
		{"action": "test_report", "report_path": "report.xml", "content": "captured"},
		{"action": "test_report", "report_path": "report.xml", "cwd": "repo"},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "developer_view", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid developer_view accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid developer_view reached handler: calls=%d want=%d", calls, len(valid))
	}
}
