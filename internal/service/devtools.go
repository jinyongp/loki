package service

import (
	"context"
	"encoding/json"
	"errors"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/devtools"
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
			Grant: controlpolicy.Agent,
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

type DevtoolsMetadataReader interface {
	InspectProject(context.Context, string) (devtools.ProjectMetadata, error)
	ListCommands(context.Context, string) (devtools.CommandCatalog, error)
	InspectCommand(context.Context, string, string) (devtools.CommandDetail, error)
}

type devtoolsDirectoryRequest struct {
	CWD string `json:"cwd"`
}

type devtoolsCommandRequest struct {
	CWD  string `json:"cwd"`
	Name string `json:"name"`
}

func DevtoolsMetadataOperations(reader DevtoolsMetadataReader) map[string]rpc.Operation {
	call := func(handle func(context.Context, string) (any, error)) rpc.Operation {
		return rpc.Operation{Grant: controlpolicy.Agent, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if reader == nil {
				return nil, errors.New("devtools metadata is unavailable")
			}
			request, err := rpc.Decode[devtoolsDirectoryRequest](raw)
			if err != nil {
				return nil, err
			}
			return handle(ctx, request.CWD)
		}}
	}
	return map[string]rpc.Operation{
		"devtools_project_inspect": call(func(ctx context.Context, cwd string) (any, error) {
			return reader.InspectProject(ctx, cwd)
		}),
		"devtools_command_list": call(func(ctx context.Context, cwd string) (any, error) {
			return reader.ListCommands(ctx, cwd)
		}),
		"devtools_command_inspect": {
			Grant: controlpolicy.Agent,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				if reader == nil {
					return nil, errors.New("devtools metadata is unavailable")
				}
				request, err := rpc.Decode[devtoolsCommandRequest](raw)
				if err != nil {
					return nil, err
				}
				return reader.InspectCommand(ctx, request.CWD, request.Name)
			},
		},
	}
}
