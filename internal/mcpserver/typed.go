package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
)

// Typed decodes arguments after the captured schema applies validation/defaults.
func Typed[T any](handler func(context.Context, T) (*mcp.CallToolResult, error)) Handler {
	return func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
		encoded, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		var request T
		if err = json.Unmarshal(encoded, &request); err != nil {
			return nil, fault.Error("invalid arguments: request; inspect the tool schema and retry")
		}
		return handler(ctx, request)
	}
}

func Require[T any](value *T, name string) (T, error) {
	if value == nil {
		var zero T
		return zero, fault.Error(name + " is required for this action")
	}
	return *value, nil
}
