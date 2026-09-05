package service

import (
	"context"
	"encoding/json"

	"loki/internal/action"
	"loki/internal/rpc"
)

type actionProcessInput struct {
	SessionID string `json:"session_id"`
	Offset    *int64 `json:"offset"`
	Limit     *int   `json:"limit"`
}

func ActionOperations(runtime *action.Runtime) map[string]rpc.Operation {
	return map[string]rpc.Operation{
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
