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
