package service

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"loki/internal/action"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/rpc"
)

type actionProcessInput struct {
	SessionID string `json:"session_id"`
	Offset    *int64 `json:"offset"`
	Limit     *int   `json:"limit"`
}

func ActionOperations(runtime *action.Runtime) map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"local_callback_bind": {Permission: rpc.Agent, Handle: runtimeTyped(func(_ context.Context, r actionProcessInput) (map[string]any, error) {
			return runtime.BindCallback(r.SessionID)
		})},
		"bootstrap_project":            {Permission: rpc.Agent, Handle: runtimeTyped(runtime.Bootstrap)},
		"clear_action_materialization": {Permission: rpc.Agent, Handle: runtimeTyped(runtime.ClearMaterialization)},
		"prepare_action":               {Permission: rpc.Agent, Handle: runtimeTyped(runtime.Prepare)},
		"run_action":                   {Permission: rpc.Agent, Handle: runtimeTyped(runtime.Run)},
		"list_processes":               {Permission: rpc.Agent, Handle: func(context.Context, json.RawMessage) (any, error) { return runtime.List(), nil }},
		"read_process": {Permission: rpc.Agent, Handle: runtimeTyped(func(_ context.Context, r actionProcessInput) (map[string]any, error) {
			limit := 65536
			if r.Limit != nil {
				limit = *r.Limit
			}
			return runtime.Read(r.SessionID, r.Offset, limit)
		})},
		"stop_process": {Permission: rpc.Agent, Handle: runtimeTyped(func(_ context.Context, r actionProcessInput) (map[string]any, error) {
			return runtime.Stop(r.SessionID)
		})},
	}
}

func BootstrapHandler(client RuntimeCaller, paths *policy.Workspace) mcpserver.Handler {
	return mcpserver.Typed(func(ctx context.Context, request action.BootstrapRequest) (*mcp.CallToolResult, error) {
		var err error
		request.CWD, err = relativeCWD(paths, request.CWD)
		if err != nil {
			return nil, err
		}
		return runtimeObject(ctx, client, struct {
			Operation string `json:"operation"`
			action.BootstrapRequest
		}{"bootstrap_project", request})
	})
}
