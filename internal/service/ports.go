package service

import (
	"context"
	"encoding/json"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/dockerproxy"
	"loki/internal/execution"
	"loki/internal/portguard"
	"loki/internal/rpc"
)

type portRequest struct{ Port int }

func ProtectedPortPolicy(mcpPort int, contract execution.Contract) (portguard.Policy, error) {
	proxyPorts, err := contract.ProxyPorts()
	if err != nil {
		return portguard.Policy{}, err
	}
	return portguard.NewPolicy(append([]int{mcpPort}, proxyPorts...)...)
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

// InspectWorkspacePort preserves guard failures unless a trusted Compose
// listener positively establishes ownership of the same port.
func InspectWorkspacePort(ctx context.Context, ports portguard.Policy, inspect func(context.Context, int) (map[string]any, error), runtime RuntimeCaller, port int) (map[string]any, error) {
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
		if runtimeDecode(ctx, runtime, map[string]any{"operation": "inspect_docker_port", "port": port}, &docker) == nil && portListener(docker) != nil {
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

func PortOperations(guard *portguard.Guard) map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"inspect": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r portRequest) (map[string]any, error) { return guard.Inspect(ctx, r.Port) })},
		"stop":    {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r portRequest) (map[string]any, error) { return guard.Stop(ctx, r.Port) })},
	}
}
func DockerOperations(inspector dockerproxy.Inspector) map[string]rpc.Operation {
	return map[string]rpc.Operation{"inspect_docker_port": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r portRequest) (map[string]any, error) {
		return inspector.Inspect(ctx, r.Port)
	})}}
}
