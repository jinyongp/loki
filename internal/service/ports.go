package service

import (
	"context"

	"loki/internal/dockerproxy"
	"loki/internal/portguard"
	"loki/internal/rpc"
)

type portRequest struct{ Port int }

func PortOperations(guard *portguard.Guard) map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"inspect": {Permission: rpc.Agent, Handle: runtimeTyped(func(ctx context.Context, r portRequest) (map[string]any, error) { return guard.Inspect(ctx, r.Port) })},
		"stop":    {Permission: rpc.Agent, Handle: runtimeTyped(func(ctx context.Context, r portRequest) (map[string]any, error) { return guard.Stop(ctx, r.Port) })},
	}
}
func DockerOperations(inspector dockerproxy.Inspector) map[string]rpc.Operation {
	return map[string]rpc.Operation{"inspect_docker_port": {Permission: rpc.Agent, Handle: runtimeTyped(func(ctx context.Context, r portRequest) (map[string]any, error) {
		return inspector.Inspect(ctx, r.Port)
	})}}
}
