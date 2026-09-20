package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	coordTaskID       = "11111111-1111-4111-8111-111111111111"
	coordRunID        = "22222222-2222-4222-8222-222222222222"
	coordWorkstreamID = "33333333-3333-4333-8333-333333333333"
)

func coordinationReadFixture(action string) map[string]any {
	base := map[string]any{"profile": "fixture", "revision": 2}
	switch action {
	case "next":
		base["item"] = map[string]any{"id": coordTaskID, "kind": "task", "workstream_id": coordWorkstreamID}
		base["reason"] = ""
		base["truncated"] = false
		base["complete"] = true
	case "task_show":
		base["item"] = map[string]any{"id": coordTaskID, "kind": "task", "running": false}
	case "workstream_show":
		base["item"] = map[string]any{"id": coordWorkstreamID, "kind": "workstream"}
	case "current", "task_history", "checkpoint_list", "workstream_list", "workstream_history":
		base["items"] = []any{}
		base["next_cursor"] = nil
		base["truncated"] = false
		base["complete"] = true
	case "task_context":
		base["item"] = map[string]any{"id": coordTaskID, "kind": "task"}
		base["documents"] = map[string]any{}
		base["tasks"] = []any{}
		base["validations"] = []any{}
		base["history"] = []any{}
		base["truncated"] = false
		base["omitted_ids"] = []any{}
		base["complete"] = true
	case "workstream_context":
		base["item"] = map[string]any{"id": coordWorkstreamID, "kind": "workstream"}
		base["documents"] = map[string]any{}
		base["tasks"] = []any{}
		base["validations"] = []any{}
		base["history"] = []any{}
		base["truncated"] = false
		base["omitted_ids"] = []any{}
		base["complete"] = true
	}
	return base
}

func TestProjectCoordinationSchemaRejectsIrrelevantTargetsBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	handlers["project_coordination"] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
		calls++
		return Object(coordinationReadFixture(input["action"].(string)))
	}
	client := connect(t, handlers)

	valid := []map[string]any{
		{"action": "next"},
		{"action": "next", "workstream_id": coordWorkstreamID},
		{"action": "task_show", "task_id": coordTaskID},
		{"action": "current", "cursor": "cursor-1", "limit": 25},
		{"action": "task_context", "task_id": coordTaskID},
		{"action": "task_history", "task_id": coordTaskID, "limit": 25},
		{"action": "checkpoint_list", "run_id": coordRunID, "limit": 25},
		{"action": "workstream_list", "limit": 25},
		{"action": "workstream_show", "workstream_id": coordWorkstreamID},
		{"action": "workstream_context", "workstream_id": coordWorkstreamID},
		{"action": "workstream_history", "workstream_id": coordWorkstreamID, "cursor": "cursor-2"},
	}
	for _, arguments := range valid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "project_coordination", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("valid project_coordination rejected: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("valid calls = %d want %d", calls, len(valid))
	}

	invalid := []map[string]any{
		{"action": "next", "task_id": coordTaskID},
		{"action": "task_show"},
		{"action": "task_show", "task_id": coordTaskID, "cursor": "x"},
		{"action": "current", "task_id": coordTaskID},
		{"action": "task_context", "task_id": coordTaskID, "limit": 1},
		{"action": "task_history", "limit": 1},
		{"action": "checkpoint_list", "task_id": coordTaskID},
		{"action": "workstream_list", "workstream_id": coordWorkstreamID},
		{"action": "workstream_show", "workstream_id": coordWorkstreamID, "cursor": "x"},
		{"action": "workstream_context", "task_id": coordTaskID},
		{"action": "workstream_history", "cursor": "x"},
		{"action": "task_show", "task_id": "bad"},
		{"action": "current", "limit": 201},
	}
	for _, arguments := range invalid {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "project_coordination", Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("invalid project_coordination accepted: args=%#v result=%#v err=%v", arguments, result, err)
		}
	}
	if calls != len(valid) {
		t.Fatalf("invalid project_coordination reached handler: calls=%d want=%d", calls, len(valid))
	}
}
