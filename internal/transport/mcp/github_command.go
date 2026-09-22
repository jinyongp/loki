package mcptransport

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
)

type githubCommandMCPRequest struct {
	Target  string   `json:"target"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Input   string   `json:"input"`
}

func GitHubCommandHandlers(runtime rpc.Caller) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{"github": mcpserver.Typed(func(ctx context.Context, request githubCommandMCPRequest) (*mcp.CallToolResult, error) {
		arguments := make([]string, 0, len(request.Args)+1)
		arguments = append(arguments, request.Command)
		arguments = append(arguments, request.Args...)
		return runtimeObject(ctx, runtime, map[string]any{
			"operation": "github_command", "target": request.Target,
			"args": arguments, "input": request.Input,
		})
	})}
}
