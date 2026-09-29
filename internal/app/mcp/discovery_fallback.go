package mcpapp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// rejectModernDiscovery keeps Loki on the legacy stateful MCP lifecycle.
// Loki binds coordination state to server-issued MCP session IDs, while the
// 2026-07-28 protocol is sessionless. Returning MethodNotFound is the standard
// compatibility signal for modern clients to fall back to initialize.
func rejectModernDiscovery(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		if method == "server/discover" {
			return nil, &jsonrpc.Error{
				Code:    jsonrpc.CodeMethodNotFound,
				Message: "method not found",
			}
		}
		return next(ctx, method, request)
	}
}
