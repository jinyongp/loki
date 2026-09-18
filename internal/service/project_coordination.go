package service

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/devtools"
	"loki/internal/mcpserver"
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

func ProjectCoordinationHandlers(runtime RuntimeCaller, sessions *DevtoolsSessionCoordination) map[string]mcpserver.Handler {
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
		return runtimeObject(ctx, runtime, map[string]any{
			"operation": operation, "cwd": request.CWD, "task_id": request.TaskID, "run_id": request.RunID,
			"workstream_id": request.WorkstreamID, "cursor": request.Cursor, "limit": request.Limit,
		})
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
		return mcpserver.Object(result)
	})
	return map[string]mcpserver.Handler{"project_coordination": read, "project_coordination_write": write}
}
