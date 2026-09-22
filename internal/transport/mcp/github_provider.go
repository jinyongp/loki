package mcptransport

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
)

type githubProviderMCPRequest struct {
	Action    string `json:"action"`
	Target    string `json:"target"`
	Number    int64  `json:"number"`
	Body      string `json:"body"`
	RequestID string `json:"request_id"`
}

func GitHubProviderHandlers(runtime rpc.Caller) map[string]mcpserver.Handler {
	read := mcpserver.Typed(func(ctx context.Context, request githubProviderMCPRequest) (*mcp.CallToolResult, error) {
		if request.Action != "repository" && request.Action != "issue" && request.Action != "pull_request" {
			return nil, errors.New("GitHub provider read action is invalid")
		}
		payload := map[string]any{
			"operation": "github_provider_read", "action": request.Action, "target": request.Target,
		}
		if request.Action != "repository" {
			payload["number"] = request.Number
		}
		return runtimeObject(ctx, runtime, payload)
	})
	write := mcpserver.Typed(func(ctx context.Context, request githubProviderMCPRequest) (*mcp.CallToolResult, error) {
		if request.Action != "comment" {
			return nil, errors.New("GitHub provider write operation is invalid")
		}
		return runtimeObject(ctx, runtime, map[string]any{
			"operation": "github_provider_comment", "action": request.Action, "target": request.Target,
			"number": request.Number, "body": request.Body, "request_id": request.RequestID,
		})
	})
	return map[string]mcpserver.Handler{"github_read": read, "github_write": write}
}
