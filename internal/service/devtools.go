package service

import (
	"context"
	"encoding/json"

	"loki/internal/rpc"
)

// DevtoolsCaller is the runtime-owned boundary around the approved devtools
// catalog. Implementations validate the command and its schema before launch.
type DevtoolsCaller interface {
	Call(context.Context, string, json.RawMessage, string, []string) (json.RawMessage, error)
}

type devtoolsRequest struct {
	Command string          `json:"command"`
	Input   json.RawMessage `json:"input"`
	Profile string          `json:"profile"`
	Secrets []string        `json:"secrets"`
}

// DevtoolsOperations exposes one authenticated runtime operation rather than
// granting the MCP process access to the binary, its state, or the secret vault.
func DevtoolsOperations(caller DevtoolsCaller) map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"devtools_call": {
			Permission: rpc.Agent,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[devtoolsRequest](raw)
				if err != nil {
					return nil, err
				}
				result, err := caller.Call(ctx, request.Command, request.Input, request.Profile, request.Secrets)
				if err != nil {
					return nil, err
				}
				return result, nil
			},
		},
	}
}
