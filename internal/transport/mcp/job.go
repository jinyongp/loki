package mcptransport

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/work/jobs"
)

type jobMCPRequest struct {
	Action         string                 `json:"action"`
	RequestID      string                 `json:"request_id"`
	CWD            string                 `json:"cwd"`
	Argv           []string               `json:"argv"`
	TimeoutSeconds int                    `json:"timeout_seconds"`
	Network        jobs.NetworkProfile    `json:"network"`
	Endpoints      []jobs.EndpointRequest `json:"endpoints"`
	JobID          string                 `json:"job_id"`
}

type JobToolchainResolver interface {
	Resolve(context.Context, string) ([]jobs.ToolchainRef, error)
}

func jobStatusResult(status jobs.Status) map[string]any {
	result := map[string]any{
		"job_id": status.JobID, "state": status.State,
		"created_at": status.CreatedAt, "updated_at": status.UpdatedAt, "deadline_at": status.DeadlineAt,
		"network": status.Network, "endpoints": append([]jobs.EndpointLease{}, status.Endpoints...),
		"toolchains": append([]jobs.ToolchainRef{}, status.Toolchains...), "truncated": status.Truncated,
	}
	if status.Outcome != "" {
		result["outcome"] = status.Outcome
	}
	if status.ExitCode != nil {
		result["exit_code"] = *status.ExitCode
	}
	if status.Cleanup != "" {
		result["cleanup"] = status.Cleanup
	}
	return result
}

func JobHandlers(controller jobs.Controller, toolchains JobToolchainResolver) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"job": mcpserver.Typed(func(ctx context.Context, request jobMCPRequest) (*mcp.CallToolResult, error) {
			if controller == nil {
				return nil, fault.New(
					fault.CodeUnavailable, "Job execution is not configured",
					false, "configure the executor peer before exposing the job tool",
				)
			}
			switch request.Action {
			case "start":
				var selected []jobs.ToolchainRef
				var err error
				if toolchains != nil {
					selected, err = toolchains.Resolve(ctx, request.CWD)
					if err != nil {
						return nil, err
					}
				}
				normalized, _, err := jobs.NormalizeStartRequest(jobs.StartRequest{
					RequestID: request.RequestID, CWD: request.CWD,
					Argv: append([]string(nil), request.Argv...), TimeoutSeconds: request.TimeoutSeconds,
					Network: request.Network, Endpoints: append([]jobs.EndpointRequest(nil), request.Endpoints...),
					Toolchains: append([]jobs.ToolchainRef(nil), selected...),
				})
				if err != nil {
					return nil, err
				}
				result, err := controller.Start(ctx, normalized)
				if err != nil {
					if errors.Is(err, jobs.ErrReplayConflict) || err.Error() == jobs.ErrReplayConflict.Error() {
						return nil, fault.New(
							fault.CodeConflict, jobs.ErrReplayConflict.Error(), false,
							"use the original start input or choose a new request_id",
						)
					}
					return nil, err
				}
				return objectResult(map[string]any{
					"action": "start", "request_id": result.RequestID, "job_id": result.JobID,
					"state": result.State, "replayed": result.Replayed, "detached": result.Detached,
					"deadline_at": result.DeadlineAt, "network": normalized.Network,
					"endpoint_requests": append([]jobs.EndpointRequest{}, normalized.Endpoints...),
					"toolchains":        append([]jobs.ToolchainRef{}, normalized.Toolchains...),
				}, nil)
			case "inspect":
				result, err := controller.Inspect(ctx, request.JobID)
				if err != nil {
					return nil, err
				}
				value := jobStatusResult(result)
				value["action"] = "inspect"
				return objectResult(value, nil)
			case "output":
				result, err := controller.Output(ctx, request.JobID)
				if err != nil {
					return nil, err
				}
				return objectResult(map[string]any{
					"action": "output", "job_id": result.JobID, "state": result.State,
					"output": result.Output, "truncated": result.Truncated, "complete": result.Complete,
				}, nil)
			case "cancel":
				result, err := controller.Cancel(ctx, request.JobID)
				if err != nil {
					return nil, err
				}
				return objectResult(map[string]any{
					"action": "cancel", "job_id": result.JobID, "canceled": result.Canceled,
					"status": jobStatusResult(result.Status),
				}, nil)
			default:
				return nil, fault.New(
					fault.CodeInvalidInput, "job action must be start, inspect, output, or cancel",
					false, "choose one action from the job tool schema",
				)
			}
		}),
	}
}
