package management

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func (b *DockerFullBackend) childNetworks(ctx context.Context, r DeploymentReservation) ([]string, error) {
	data, err := b.command(ctx, "listing deployment job networks", nil, "network", "ls", "--filter", "label="+fullOwnerLabel+"="+b.Store.FullOwner(), "--filter", "label="+fullDeploymentLabel+"="+r.ID, "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	ids, err := dockerLines(data)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !dockerIDPattern.MatchString(id) {
			return nil, fmt.Errorf("invalid owned network ID")
		}
	}
	return ids, nil
}

func (b *DockerFullBackend) removeChildNetwork(ctx context.Context, r DeploymentReservation, id string) error {
	data, err := b.command(ctx, "verifying deployment job network", nil, "network", "inspect", "--format", "{{json .}}", id)
	if err != nil {
		return err
	}
	var network struct {
		Name       string
		Labels     map[string]string
		Containers map[string]json.RawMessage
	}
	if err := json.Unmarshal(data, &network); err != nil {
		return err
	}
	l := network.Labels
	if l[fullOwnerLabel] != b.Store.FullOwner() || l[fullDeploymentLabel] != r.ID || l["io.loki.owner"] != "job" || !deploymentDigestPattern.MatchString(l["io.loki.policy.sha256"]) || !deploymentDigestPattern.MatchString(l["io.loki.sandbox.sha256"]) || !slices.Contains([]string{"internal-network", "outbound-network"}, l["io.loki.resource.component"]) || !strings.HasPrefix(network.Name, "loki-job-") {
		return fmt.Errorf("network has unknown deployment ownership; preserving it")
	}
	jobID := l["io.loki.job.id"]
	prefix := "loki-job-net-"
	if l["io.loki.resource.component"] == "outbound-network" {
		prefix = "loki-job-egress-"
	}
	if len(jobID) != 32 || !deploymentDigestPattern.MatchString(jobID+jobID) || network.Name != prefix+jobID {
		return fmt.Errorf("network name differs from its owned job identity; preserving it")
	}
	if len(network.Containers) != 0 {
		return fmt.Errorf("deployment job network still has attached containers; generation remains reserved")
	}
	_, err = b.command(ctx, "removing stopped deployment job network", nil, "network", "rm", id)
	return err
}

// First stop all service processes, including the launcher that could create
// children. Inspect every selected object before removing any of them. A failed
// daemon request never becomes proof that retained generations are unused.
func (b *DockerFullBackend) Stop(ctx context.Context, r DeploymentReservation) error {
	ids, err := b.containerIDs(ctx, r)
	if err != nil {
		return err
	}
	services := []string{}
	for _, id := range ids {
		c, err := b.inspectContainer(ctx, id)
		if err != nil {
			return err
		}
		if err := b.requireParent(r, c); err != nil {
			return err
		}
		if c.Config.Labels[fullServiceLabel] != "" {
			services = append(services, id)
		}
	}
	for _, id := range services {
		if _, err := b.command(ctx, "stopping an owned service", nil, "container", "stop", "--time", "10", id); err != nil {
			return err
		}
	}
	// Re-list after stopping the launcher to include children of in-flight starts.
	ids, err = b.containerIDs(ctx, r)
	if err != nil {
		return err
	}
	for _, id := range ids {
		c, err := b.inspectContainer(ctx, id)
		if err != nil {
			return err
		}
		if err := b.requireParent(r, c); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if _, err := b.command(ctx, "removing an owned deployment container", nil, "container", "rm", "--force", id); err != nil {
			return err
		}
	}
	networks, err := b.childNetworks(ctx, r)
	if err != nil {
		return err
	}
	for _, id := range networks {
		if err := b.removeChildNetwork(ctx, r, id); err != nil {
			return err
		}
	}
	return b.ConfirmAbsent(ctx, r)
}

func (b *DockerFullBackend) ConfirmAbsent(ctx context.Context, r DeploymentReservation) error {
	if !deploymentIDPattern.MatchString(r.ID) {
		return fmt.Errorf("invalid deployment absence request")
	}
	containers, err := b.containerIDs(ctx, r)
	if err != nil {
		return err
	}
	networks, err := b.childNetworks(ctx, r)
	if err != nil {
		return err
	}
	if len(containers) != 0 || len(networks) != 0 {
		return fmt.Errorf("deployment still retains service or job resources; generation remains reserved")
	}
	return nil
}
