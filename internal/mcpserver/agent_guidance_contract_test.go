package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func guidanceFixture() map[string]any {
	return map[string]any{
		"guidance": map[string]any{
			"target": "src/new.go", "target_dir": "src",
			"revision": strings.Repeat("a", 64), "complete": true,
			"sources": []any{map[string]any{
				"path": "AGENTS.md", "scope": ".", "revision": strings.Repeat("b", 64),
				"bytes": 10, "content": "root rules",
			}},
			"diagnostics": []any{}, "total_bytes": 10,
		},
		"skills": map[string]any{
			"complete": true,
			"items": []any{map[string]any{
				"name": "review-skill", "description": "Use for reviews.", "scope": "project",
				"revision": strings.Repeat("c", 64), "resource_count": 1, "total_bytes": 128,
			}},
			"diagnostics": []any{}, "shadowed": []any{},
		},
	}
}

func skillFixture() map[string]any {
	return map[string]any{
		"skill": map[string]any{
			"item": map[string]any{
				"name": "review-skill", "description": "Use for reviews.", "scope": "project",
				"revision": strings.Repeat("c", 64), "resource_count": 1, "total_bytes": 128,
				"content": "# Review",
				"resources": []any{map[string]any{
					"path": "references/checks.md", "size": 12, "sha256": strings.Repeat("d", 64), "executable": false,
				}},
			},
			"diagnostics": []any{}, "shadowed": []any{},
		},
		"complete": true,
	}
}

func TestAgentGuidanceSchemaRejectsVariantLeaksBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["agent_guidance"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		if input["action"] == "skill" {
			return Object(skillFixture())
		}
		return Object(guidanceFixture())
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{"action": "context"},
		{"action": "context", "cwd": "repo", "target": "src/new.go"},
		{"action": "skill", "name": "review-skill"},
		{"action": "skill", "cwd": "repo", "target": "src/new.go", "name": "review-skill"},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "agent_guidance", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid agent_guidance rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid agent_guidance calls = %d want %d", calls, len(valid))
	}

	invalid := []map[string]any{
		{"action": "context", "name": "review-skill"},
		{"action": "skill"},
		{"action": "skill", "name": "Bad Skill"},
		{"action": "context", "unknown": true},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "agent_guidance", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid agent_guidance accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid agent_guidance reached handler: calls=%d want=%d", calls, len(valid))
	}
}
