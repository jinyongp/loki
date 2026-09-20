package service

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/agentcontext"
	"loki/internal/mcpserver"
)

type agentGuidanceRequest struct {
	Action string `json:"action"`
	CWD    string `json:"cwd"`
	Target string `json:"target"`
	Name   string `json:"name"`
}

type AgentGuidanceProvider interface {
	Context(context.Context, string, string) (agentcontext.ContextResult, error)
	Skill(context.Context, string, string, string) (agentcontext.SkillInspection, error)
}

func AgentGuidanceHandlers(provider AgentGuidanceProvider) map[string]mcpserver.Handler {
	handler := mcpserver.Typed(func(ctx context.Context, request agentGuidanceRequest) (*mcp.CallToolResult, error) {
		if provider == nil {
			return nil, errors.New("agent guidance is unavailable")
		}
		if request.CWD == "" {
			request.CWD = "."
		}
		switch request.Action {
		case "context":
			if request.Name != "" {
				return nil, errors.New("agent guidance context does not accept a Skill name")
			}
			if request.Target == "" {
				request.Target = "."
			}
			result, err := provider.Context(ctx, request.CWD, request.Target)
			if err != nil {
				return nil, err
			}
			return mcpserver.Object(map[string]any{
				"guidance": result.Guidance,
				"skills":   result.Skills,
			})
		case "skill":
			if request.Name == "" {
				return nil, errors.New("agent guidance skill requires a name")
			}
			if request.Target == "" {
				request.Target = "."
			}
			result, err := provider.Skill(ctx, request.CWD, request.Target, request.Name)
			if err != nil {
				return nil, err
			}
			return mcpserver.Object(map[string]any{"skill": result, "complete": true})
		default:
			return nil, errors.New("agent guidance action must be context or skill")
		}
	})
	return map[string]mcpserver.Handler{"agent_guidance": handler}
}
