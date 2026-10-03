package management

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
)

func (b *DockerFullBackend) Connection(ctx context.Context) (FullConnection, error) {
	plan, err := b.Store.PlanFull()
	if err != nil {
		return FullConnection{}, err
	}
	reservations, err := b.Store.Deployments()
	if err != nil {
		return FullConnection{}, err
	}
	for _, reservation := range reservations {
		if reservation.PlanDigest != fullPlanDigest(plan) {
			continue
		}
		record, err := b.record(reservation)
		if err != nil {
			return FullConnection{}, err
		}
		for _, spec := range record.Specs {
			if spec.Service != "mcp" {
				continue
			}
			inspection, err := b.inspectContainer(ctx, spec.Name)
			if err != nil {
				return FullConnection{}, err
			}
			if err := b.verifySpec(reservation, spec, inspection); err != nil {
				return FullConnection{}, err
			}
			if !inspection.State.Running {
				return FullConnection{}, fmt.Errorf("MCP service is stopped; run loki tools start")
			}
			bindings := inspection.NetworkSettings.Ports["18765/tcp"]
			if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" {
				return FullConnection{}, fmt.Errorf("MCP service does not have its owned loopback publication")
			}
			port, err := strconv.Atoi(bindings[0].HostPort)
			if err != nil || port < 1024 || port > 65535 {
				return FullConnection{}, fmt.Errorf("MCP service has an invalid loopback port")
			}
			current, err := b.Store.PlanFull()
			if err != nil || fullPlanDigest(current) != reservation.PlanDigest {
				return FullConnection{}, fmt.Errorf("selection changed while connecting; reconnect the MCP server")
			}
			return FullConnection{Endpoint: "http://127.0.0.1:" + strconv.Itoa(port) + "/mcp", TokenFile: filepath.Join(b.Store.Root, "auth", "mcp", "token")}, nil
		}
	}
	return FullConnection{}, fmt.Errorf("selected MCP service has not started; run loki tools start")
}
