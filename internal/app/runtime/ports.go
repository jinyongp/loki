package runtime

import (
	"context"

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

func PortOperations(guard *portguard.Guard) map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"inspect": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r portRequest) (map[string]any, error) { return guard.Inspect(ctx, r.Port) })},
		"stop":    {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r portRequest) (map[string]any, error) { return guard.Stop(ctx, r.Port) })},
	}
}

func DockerOperations(inspector dockerproxy.Inspector) map[string]rpc.Operation {
	return map[string]rpc.Operation{"inspect_docker_port": {
		Grant: controlpolicy.Agent,
		Handle: runtimeTyped(func(ctx context.Context, r portRequest) (map[string]any, error) {
			return inspector.Inspect(ctx, r.Port)
		}),
	}}
}
