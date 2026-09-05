package service

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/skills"
)

type skillRead struct {
	Action, CWD string
	Name, Path  *string
}

type skillWrite struct {
	Action, Scope, CWD, Name                        string
	Description, Instructions, Path, Content, Patch *string
	Expected                                        *string `json:"expected_sha256"`
	Metadata                                        map[string]any
	Overwrite                                       bool
	Encoding                                        string
}

func SkillWriteHandler(registry *skills.Registry) mcpserver.Handler {
	return mcpserver.Typed(func(ctx context.Context, r skillWrite) (*mcp.CallToolResult, error) {
		switch r.Action {
		case "create":
			description, err := mcpserver.Require(r.Description, "description")
			if err != nil {
				return nil, err
			}
			instructions, err := mcpserver.Require(r.Instructions, "instructions")
			if err != nil {
				return nil, err
			}
			return objectResult(registry.Create(r.Scope, r.CWD, r.Name, description, instructions, r.Metadata))
		case "edit":
			patch, err := mcpserver.Require(r.Patch, "patch")
			if err != nil {
				return nil, err
			}
			expected, err := mcpserver.Require(r.Expected, "expected_sha256")
			if err != nil {
				return nil, err
			}
			return objectResult(registry.Edit(ctx, r.Name, r.CWD, patch, expected))
		case "resource":
			path, err := mcpserver.Require(r.Path, "path")
			if err != nil {
				return nil, err
			}
			content, err := mcpserver.Require(r.Content, "content")
			if err != nil {
				return nil, err
			}
			return objectResult(registry.WriteResource(r.Name, path, content, r.CWD, r.Overwrite, r.Expected, r.Encoding))
		default:
			return nil, fault.Error("invalid skill write action")
		}
	})
}

func SkillReadHandlers(registry *skills.Registry, tools map[string]bool) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"agent_context": mcpserver.Typed(func(_ context.Context, r struct{ CWD string }) (*mcp.CallToolResult, error) {
			return objectResult(registry.AgentContext(r.CWD))
		}),
		"skill_read": mcpserver.Typed(func(_ context.Context, r skillRead) (*mcp.CallToolResult, error) {
			if r.Action == "list" {
				return objectResult(registry.List(r.CWD))
			}
			name, err := mcpserver.Require(r.Name, "name")
			if err != nil {
				return nil, err
			}
			switch r.Action {
			case "activate":
				return objectResult(registry.Activate(name, r.CWD))
			case "validate":
				return objectResult(registry.Validate(name, r.CWD, tools))
			case "resource":
				path, err := mcpserver.Require(r.Path, "path")
				if err != nil {
					return nil, err
				}
				return objectResult(registry.ReadResource(name, path, r.CWD))
			default:
				return nil, fault.Error("invalid skill read action")
			}
		}),
	}
}
