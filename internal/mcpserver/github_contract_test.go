package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGitHubEscapeHatchSchemaRejectsUnsupportedCommandsBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["github"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		return Object(map[string]any{
			"exit_code": 0, "output": "", "truncated": false, "timed_out": false, "canceled": false,
		})
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{"target": "owner/repo", "command": "issue"},
		{"target": "owner/repo", "command": "pr", "args": []any{"list", "--limit", "1"}},
		{"target": "owner/repo", "command": "api", "args": []any{"repos/{owner}/{repo}/issues"}, "input": "{}"},
		{"target": "owner/repo", "command": "search", "args": []any{"issues", "is:open"}},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "github", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid github call rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid github handler calls = %d want %d", calls, len(valid))
	}

	invalid := []map[string]any{
		{"target": "owner/repo", "args": []any{"issue", "list"}},
		{"target": "owner/repo", "command": "auth"},
		{"target": "owner/repo", "command": "extension", "args": []any{"exec", "x"}},
		{"target": "owner/repo", "command": "issue", "args": []any{"list"}, "token": "secret"},
		{"target": "bad target", "command": "issue"},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "github", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid github call accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid github calls reached handler: calls=%d want=%d", calls, len(valid))
	}
}
