package management

import (
	"context"
	"fmt"
	"os"
	"strings"
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
			if len(inspection.ID) != 64 || !dockerIDPattern.MatchString(inspection.ID) || spec.User != "10000:10000" || len(spec.Command) == 0 || spec.Publish != "" {
				return FullConnection{}, fmt.Errorf("MCP connection requires its owned non-root role and private stdio adapter")
			}
			current, err := b.Store.PlanFull()
			if err != nil || fullPlanDigest(current) != reservation.PlanDigest {
				return FullConnection{}, fmt.Errorf("selection changed while connecting; reconnect the MCP server")
			}
			environment := []string{}
			for _, value := range os.Environ() {
				key, _, _ := strings.Cut(value, "=")
				if key != "DOCKER_HOST" && key != "DOCKER_CONTEXT" && key != "DOCKER_API_VERSION" {
					environment = append(environment, value)
				}
			}
			return FullConnection{Command: b.dockerCommand("exec", "--interactive", "--user", spec.User, inspection.ID, spec.Command[0], "full-mcp-connect"), Environment: append(environment, "DOCKER_API_VERSION=1.47")}, nil
		}
	}
	return FullConnection{}, fmt.Errorf("selected MCP service has not started; run loki tools start")
}
