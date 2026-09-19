package service

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/devtools"
	"loki/internal/mcpserver"
)

type agentGuidanceRequest struct {
	Action string `json:"action"`
	CWD    string `json:"cwd"`
	Target string `json:"target"`
	Name   string `json:"name"`
}

func AgentGuidanceHandlers(runtime RuntimeCaller) map[string]mcpserver.Handler {
	handler := mcpserver.Typed(func(ctx context.Context, request agentGuidanceRequest) (*mcp.CallToolResult, error) {
		if runtime == nil {
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
			var guidance devtools.GuidanceResult
			if err := runtimeDecode(ctx, runtime, map[string]any{
				"operation": "devtools_guidance_resolve",
				"cwd":       request.CWD,
				"target":    request.Target,
			}, &guidance); err != nil {
				return nil, err
			}
			var skills devtools.SkillCatalog
			if err := runtimeDecode(ctx, runtime, map[string]any{
				"operation": "devtools_skill_list",
				"cwd":       request.CWD,
			}, &skills); err != nil {
				return nil, err
			}
			return mcpserver.Object(map[string]any{
				"guidance": guidance,
				"skills":   skills,
			})
		case "skill":
			if request.Name == "" {
				return nil, errors.New("agent guidance skill requires a name")
			}
			if request.Target != "" {
				return nil, errors.New("agent guidance skill does not accept a target")
			}
			var skill devtools.SkillInspection
			if err := runtimeDecode(ctx, runtime, map[string]any{
				"operation": "devtools_skill_inspect",
				"cwd":       request.CWD,
				"name":      request.Name,
			}, &skill); err != nil {
				return nil, err
			}
			return mcpserver.Object(map[string]any{"skill": skill})
		default:
			return nil, errors.New("agent guidance action must be context or skill")
		}
	})
	return map[string]mcpserver.Handler{"agent_guidance": handler}
}
