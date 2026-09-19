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

type DevtoolsCoordinationReader interface {
	QueryCoordination(context.Context, string, devtools.CoordinationQuery, devtools.CoordinationRequest) (devtools.CoordinationProjection, error)
}

type devtoolsCoordinationRequest struct {
	CWD          string `json:"cwd"`
	TaskID       string `json:"task_id"`
	WorkstreamID string `json:"workstream_id"`
	RunID        string `json:"run_id"`
	Cursor       string `json:"cursor"`
	Limit        int    `json:"limit"`
}

func DevtoolsCoordinationOperations(reader DevtoolsCoordinationReader) map[string]rpc.Operation {
	type spec struct {
		query            devtools.CoordinationQuery
		target           string
		page             bool
		workstreamFilter bool
	}
	specs := map[string]spec{
		"devtools_task_next":          {query: devtools.CoordinationTaskNext, workstreamFilter: true},
		"devtools_task_show":          {query: devtools.CoordinationTaskShow, target: "task"},
		"devtools_task_current":       {query: devtools.CoordinationTaskCurrent, page: true},
		"devtools_task_context":       {query: devtools.CoordinationTaskContext, target: "task"},
		"devtools_task_history":       {query: devtools.CoordinationTaskHistory, target: "task", page: true},
		"devtools_checkpoint_list":    {query: devtools.CoordinationCheckpointList, target: "run", page: true},
		"devtools_workstream_list":    {query: devtools.CoordinationWorkstreamList, page: true},
		"devtools_workstream_show":    {query: devtools.CoordinationWorkstreamShow, target: "workstream"},
		"devtools_workstream_context": {query: devtools.CoordinationWorkstreamContext, target: "workstream"},
		"devtools_workstream_history": {query: devtools.CoordinationWorkstreamHistory, target: "workstream", page: true},
	}
	operations := make(map[string]rpc.Operation, len(specs))
	for name, definition := range specs {
		definition := definition
		operations[name] = rpc.Operation{Grant: controlpolicy.Agent, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if reader == nil {
				return nil, errors.New("devtools coordination is unavailable")
			}
			request, err := rpc.Decode[devtoolsCoordinationRequest](raw)
			if err != nil {
				return nil, err
			}
			target := ""
			switch definition.target {
			case "task":
				target = request.TaskID
			case "workstream":
				target = request.WorkstreamID
			case "run":
				target = request.RunID
			}
			if definition.target != "" && target == "" {
				return nil, errors.New("devtools coordination target is required")
			}
			if definition.target != "task" && request.TaskID != "" ||
				definition.target != "run" && request.RunID != "" ||
				definition.target != "workstream" && !definition.workstreamFilter && request.WorkstreamID != "" {
				return nil, errors.New("devtools coordination request contains an unrelated target")
			}
			if !definition.page && (request.Cursor != "" || request.Limit != 0) {
				return nil, errors.New("devtools coordination request does not support pagination")
			}
			if request.Limit < 0 || request.Limit > 200 {
				return nil, errors.New("devtools coordination limit is invalid")
			}
			filter := ""
			if definition.workstreamFilter {
				filter = request.WorkstreamID
			}
			return reader.QueryCoordination(ctx, request.CWD, definition.query, devtools.CoordinationRequest{
				Target: target, Workstream: filter, Cursor: request.Cursor, Limit: request.Limit,
			})
		}}
	}
	return operations
}

type DevtoolsAgentGuidanceReader interface {
	ListSkills(context.Context, string) (devtools.SkillCatalog, error)
	InspectSkill(context.Context, string, string) (devtools.SkillInspection, error)
	ResolveGuidance(context.Context, string, string) (devtools.GuidanceResult, error)
}

type devtoolsSkillRequest struct {
	CWD  string `json:"cwd"`
	Name string `json:"name"`
}

type devtoolsGuidanceRequest struct {
	CWD    string `json:"cwd"`
	Target string `json:"target"`
}

func DevtoolsAgentGuidanceOperations(reader DevtoolsAgentGuidanceReader) map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"devtools_skill_list": {
			Grant: controlpolicy.Agent,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				if reader == nil {
					return nil, errors.New("devtools Skill inventory is unavailable")
				}
				request, err := rpc.Decode[devtoolsDirectoryRequest](raw)
				if err != nil {
					return nil, err
				}
				return reader.ListSkills(ctx, request.CWD)
			},
		},
		"devtools_skill_inspect": {
			Grant: controlpolicy.Agent,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				if reader == nil {
					return nil, errors.New("devtools Skill inventory is unavailable")
				}
				request, err := rpc.Decode[devtoolsSkillRequest](raw)
				if err != nil {
					return nil, err
				}
				if request.Name == "" {
					return nil, errors.New("devtools Skill name is required")
				}
				return reader.InspectSkill(ctx, request.CWD, request.Name)
			},
		},
		"devtools_guidance_resolve": {
			Grant: controlpolicy.Agent,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				if reader == nil {
					return nil, errors.New("devtools guidance resolver is unavailable")
				}
				request, err := rpc.Decode[devtoolsGuidanceRequest](raw)
				if err != nil {
					return nil, err
				}
				if request.Target == "" {
					return nil, errors.New("devtools guidance target is required")
				}
				return reader.ResolveGuidance(ctx, request.CWD, request.Target)
			},
		},
	}
}
