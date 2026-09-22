package mcptransport

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/devtools"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
)

type projectCoordinationRequest struct {
	Action       string `json:"action"`
	CWD          string `json:"cwd"`
	TaskID       string `json:"task_id"`
	RunID        string `json:"run_id"`
	WorkstreamID string `json:"workstream_id"`
	Cursor       string `json:"cursor"`
	Limit        int    `json:"limit"`
}

type projectCoordinationWriteRequest struct {
	Action              string   `json:"action"`
	CWD                 string   `json:"cwd"`
	TaskID              string   `json:"task_id"`
	RunID               string   `json:"run_id"`
	WorkstreamID        string   `json:"workstream_id"`
	ExpectedRunID       string   `json:"expected_run_id"`
	RequestID           string   `json:"request_id"`
	Summary             string   `json:"summary"`
	Decisions           []string `json:"decisions"`
	ValidationRecordIDs []string `json:"validation_record_ids"`
	Remaining           []string `json:"remaining"`
	NextAction          string   `json:"next_action"`
	Blockers            []string `json:"blockers"`
}

func coordinationRawValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return json.RawMessage(append([]byte(nil), raw...))
}

func coordinationRawList(values []json.RawMessage) []json.RawMessage {
	if len(values) == 0 {
		return []json.RawMessage{}
	}
	return append([]json.RawMessage(nil), values...)
}

func coordinationCursor(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func projectCoordinationReadResult(action string, projection devtools.CoordinationProjection) map[string]any {
	base := map[string]any{"profile": projection.Profile, "revision": projection.Revision}
	switch action {
	case "next":
		base["item"] = coordinationRawValue(projection.Item)
		base["reason"] = projection.Reason
		base["truncated"] = projection.Truncated
		base["complete"] = !projection.Truncated
	case "task_show", "workstream_show":
		base["item"] = coordinationRawValue(projection.Item)
	case "current", "task_history", "checkpoint_list", "workstream_list", "workstream_history":
		base["items"] = coordinationRawList(projection.Items)
		base["next_cursor"] = coordinationCursor(projection.NextCursor)
		base["truncated"] = projection.Truncated
		base["complete"] = projection.NextCursor == nil && !projection.Truncated
	case "task_context", "workstream_context":
		base["item"] = coordinationRawValue(projection.Item)
		base["documents"] = coordinationRawValue(projection.Documents)
		base["tasks"] = coordinationRawList(projection.Tasks)
		base["validations"] = coordinationRawList(projection.Validations)
		base["history"] = coordinationRawList(projection.History)
		base["truncated"] = projection.Truncated
		base["omitted_ids"] = append([]string{}, projection.OmittedIDs...)
		base["complete"] = !projection.Truncated && len(projection.OmittedIDs) == 0
	}
	return base
}

func projectCoordinationWriteResult(action, requestID string, public map[string]any) map[string]any {
	result := map[string]any{"action": action, "request_id": requestID, "details": map[string]any{}}
	known := map[string]bool{
		"profile": true, "revision": true, "previous_revision": true, "current_revision": true,
		"affected_count": true, "affected_ids": true, "replayed": true, "changed": true,
		"action_ids": true, "item": true, "run": true, "claimed": true, "record_id": true,
	}
	details := result["details"].(map[string]any)
	for key, value := range public {
		if key == "request_id" {
			if text, ok := value.(string); ok && text != "" {
				result["request_id"] = text
			}
			continue
		}
		if known[key] {
			result[key] = value
			continue
		}
		details[key] = value
	}
	return result
}

func ProjectCoordinationHandlers(runtime rpc.Caller, sessions *DevtoolsSessionCoordination) map[string]mcpserver.Handler {
	read := mcpserver.Typed(func(ctx context.Context, request projectCoordinationRequest) (*mcp.CallToolResult, error) {
		operation := map[string]string{
			"next":               "devtools_task_next",
			"task_show":          "devtools_task_show",
			"current":            "devtools_task_current",
			"task_context":       "devtools_task_context",
			"task_history":       "devtools_task_history",
			"checkpoint_list":    "devtools_checkpoint_list",
			"workstream_list":    "devtools_workstream_list",
			"workstream_show":    "devtools_workstream_show",
			"workstream_context": "devtools_workstream_context",
			"workstream_history": "devtools_workstream_history",
		}[request.Action]
		if operation == "" {
			return nil, errors.New("unsupported project coordination read action")
		}
		if request.CWD == "" {
			request.CWD = "."
		}
		var projection devtools.CoordinationProjection
		if err := rpc.DecodeCall(ctx, runtime, map[string]any{
			"operation": operation, "cwd": request.CWD, "task_id": request.TaskID, "run_id": request.RunID,
			"workstream_id": request.WorkstreamID, "cursor": request.Cursor, "limit": request.Limit,
		}, &projection); err != nil {
			return nil, err
		}
		return mcpserver.Object(projectCoordinationReadResult(request.Action, projection))
	})

	write := mcpserver.Typed(func(ctx context.Context, request projectCoordinationWriteRequest) (*mcp.CallToolResult, error) {
		if sessions == nil {
			return nil, errors.New("project coordination writes are unavailable")
		}
		action := map[string]devtools.CoordinationMutation{
			"claim": devtools.CoordinationClaim, "takeover": devtools.CoordinationTakeover, "resume": devtools.CoordinationResume,
			"checkpoint": devtools.CoordinationCheckpoint, "release": devtools.CoordinationRelease, "done": devtools.CoordinationDone,
		}[request.Action]
		if action == "" {
			return nil, errors.New("unsupported project coordination write action")
		}
		if request.CWD == "" {
			request.CWD = "."
		}
		target := request.TaskID
		if action == devtools.CoordinationCheckpoint || action == devtools.CoordinationRelease {
			target = request.RunID
		}
		result, err := sessions.MutateMCP(ctx, action, DevtoolsSessionMutationRequest{
			CWD: request.CWD, TargetID: target, WorkstreamID: request.WorkstreamID, ExpectedRunID: request.ExpectedRunID,
			RequestID: request.RequestID, Summary: request.Summary, Decisions: request.Decisions,
			ValidationRecordIDs: request.ValidationRecordIDs, Remaining: request.Remaining,
			NextAction: request.NextAction, Blockers: request.Blockers,
		})
		if err != nil {
			return nil, err
		}
		return mcpserver.Object(projectCoordinationWriteResult(request.Action, request.RequestID, result))
	})
	return map[string]mcpserver.Handler{"project_coordination": read, "project_coordination_write": write}
}
