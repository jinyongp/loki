package mcptransport

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/control/appvalues"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
)

// ApplicationSecretHandlers implements the resource contract. Activation is
// owned by the host composition after the migration gate has passed.
func ApplicationSecretHandlers(client rpc.Caller) map[string]mcpserver.Handler {
	handlers := map[string]mcpserver.Handler{}
	for _, operation := range []string{"inspect", "write", "delete"} {
		handlers["secret_"+operation] = mcpserver.Typed(func(ctx context.Context, request appvalues.Request) (*mcp.CallToolResult, error) {
			if request.Validate(operation) != nil {
				return nil, fault.New(fault.CodeInvalidInput, "invalid application value request", false, "use the resource-scoped operation schema")
			}
			return runtimeObject(ctx, client, appvalues.RPCRequest{Operation: operation, Request: request})
		})
	}
	return handlers
}
