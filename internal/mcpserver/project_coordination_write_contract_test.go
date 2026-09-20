package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func coordinationWriteFixture(input map[string]any) map[string]any {
	result := map[string]any{
		"action": input["action"], "request_id": input["request_id"],
		"profile": "fixture", "revision": 3, "changed": true,
		"details": map[string]any{},
	}
	if input["action"] == "claim" || input["action"] == "takeover" {
		result["claimed"] = true
	}
	return result
}

func TestProjectCoordinationWriteSchemaRejectsTransitionLeaksBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["project_coordination_write"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		return Object(coordinationWriteFixture(input))
	}
	client := connect(t, handlers)
	request := func(n int) string {
		return []string{
			"80000000-0000-4000-8000-000000000001",
			"80000000-0000-4000-8000-000000000002",
			"80000000-0000-4000-8000-000000000003",
			"80000000-0000-4000-8000-000000000004",
			"80000000-0000-4000-8000-000000000005",
			"80000000-0000-4000-8000-000000000006",
			"80000000-0000-4000-8000-000000000007",
			"80000000-0000-4000-8000-000000000008",
		}[n]
	}
	valid := []map[string]any{
		{"action": "claim", "request_id": request(0)},
		{"action": "claim", "task_id": coordTaskID, "request_id": request(1)},
		{"action": "claim", "workstream_id": coordWorkstreamID, "request_id": request(2)},
		{"action": "takeover", "task_id": coordTaskID, "expected_run_id": coordRunID, "request_id": request(3)},
		{"action": "resume", "task_id": coordTaskID, "request_id": request(4)},
		{
			"action": "checkpoint", "run_id": coordRunID, "summary": "progress", "request_id": request(5),
			"decisions": []any{"keep typed boundary"}, "remaining": []any{"finish tests"}, "next_action": "continue",
		},
		{"action": "release", "run_id": coordRunID, "request_id": request(6), "summary": "handoff"},
		{"action": "done", "task_id": coordTaskID, "summary": "complete", "request_id": request(7)},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "project_coordination_write", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid project_coordination_write rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid write calls = %d want %d", calls, len(valid))
	}

	invalid := []map[string]any{
		{"action": "claim", "task_id": coordTaskID, "workstream_id": coordWorkstreamID, "request_id": request(0)},
		{"action": "claim", "summary": "x", "request_id": request(0)},
		{"action": "takeover", "task_id": coordTaskID, "request_id": request(1)},
		{"action": "takeover", "task_id": coordTaskID, "run_id": coordRunID, "expected_run_id": coordRunID, "request_id": request(1)},
		{"action": "resume", "request_id": request(2)},
		{"action": "resume", "task_id": coordTaskID, "summary": "x", "request_id": request(2)},
		{"action": "checkpoint", "run_id": coordRunID, "request_id": request(3)},
		{"action": "checkpoint", "summary": "x", "request_id": request(3)},
		{"action": "release", "task_id": coordTaskID, "request_id": request(4)},
		{"action": "done", "task_id": coordTaskID, "request_id": request(5)},
		{"action": "done", "task_id": coordTaskID, "summary": "done", "decisions": []any{"irrelevant"}, "request_id": request(5)},
		{"action": "claim", "request_id": "bad"},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "project_coordination_write", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid project_coordination_write accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid write call reached handler: calls=%d want=%d", calls, len(valid))
	}
}
