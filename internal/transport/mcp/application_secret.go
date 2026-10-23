package mcptransport

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/control/appvalues"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
)

// ApplicationSecretHandlers is the sole application-value MCP transport.
func ApplicationSecretHandlers(client rpc.Caller) map[string]mcpserver.Handler {
	handlers := map[string]mcpserver.Handler{}
	for _, operation := range []string{"inspect", "write", "delete"} {
		handlers["secret_"+operation] = mcpserver.Typed(func(ctx context.Context, request appvalues.Request) (*mcp.CallToolResult, error) {
			if request.Validate(operation) != nil {
				return nil, fault.New(fault.CodeInvalidInput, "invalid application value request", false, "use the resource-scoped operation schema")
			}
			if client == nil {
				return nil, fault.New(fault.CodeUnavailable, "application value service is unavailable", true, "ask the host administrator to check the dedicated secrets service")
			}
			return runtimeObject(ctx, client, appvalues.RPCRequest{Operation: operation, Request: request})
		})
	}
	return handlers
}
