package mcptransport

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/portguard"
	"loki/internal/rpc"
)

type BrowserCaller interface {
	Call(context.Context, string, map[string]any) (map[string]any, error)
}

func runtimeObject(ctx context.Context, client rpc.Caller, request any) (*mcp.CallToolResult, error) {
	if client == nil {
		return nil, fault.Error("runtime is unavailable")
	}
	data, err := client.Call(ctx, request)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil || result == nil {
		return nil, fault.Error("invalid runtime response")
	}
	return objectResult(result, nil)
}

func portListener(result map[string]any) map[string]any {
	if result["in_use"] != true {
		return nil
	}
	data, err := json.Marshal(result["listeners"])
	if err != nil {
		return nil
	}
	var rows []map[string]any
	if json.Unmarshal(data, &rows) != nil || len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// InspectWorkspacePort preserves guard failures unless a trusted runtime
// listener positively establishes ownership of the same port.
func InspectWorkspacePort(
	ctx context.Context,
	ports portguard.Policy,
	inspect func(context.Context, int) (map[string]any, error),
	runtime rpc.Caller,
	port int,
) (map[string]any, error) {
	if err := ports.Validate(port); err != nil {
		return nil, err
	}
	var guarded, docker map[string]any
	var guardErr error
	if inspect != nil {
		guarded, guardErr = inspect(ctx, port)
	}
	if portListener(guarded) != nil {
		return guarded, nil
	}
	if runtime != nil {
		if rpc.DecodeCall(ctx, runtime, map[string]any{
			"operation": "inspect_docker_port", "port": port,
		}, &docker) == nil && portListener(docker) != nil {
			return docker, nil
		}
	}
	if guardErr != nil {
		return nil, guardErr
	}
	if guarded != nil {
		return guarded, nil
	}
	if docker != nil {
		return docker, nil
	}
	return map[string]any{"port": port, "in_use": false, "listeners": []any{}}, nil
}
