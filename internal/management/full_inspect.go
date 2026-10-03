package management

import (
	"context"
	"fmt"
	"sync"

	"loki/internal/tools"
)

// FullProbes observes a matching reserved deployment once, then attributes
// actual role readiness to the public modules and their private prerequisites.
// Installed inactive resources receive no fabricated execution result.
func (s Store) FullProbes(backend FullBackend) (map[tools.ID]Probe, error) {
	plan, err := s.PlanFull()
	if err != nil {
		return nil, err
	}
	reservations, err := s.Deployments()
	if err != nil {
		return nil, err
	}
	var reservation *DeploymentReservation
	for _, current := range reservations {
		if current.PlanDigest == fullPlanDigest(plan) {
			copy := current
			reservation = &copy
			break
		}
	}
	var once sync.Once
	var observed FullObservation
	var observationError error
	roles := map[tools.ID][]string{
		"runtime-core": {"mcp"}, "workspace": {"mcp", "workspace"}, "git": {"mcp", "launcher", "executor", "git"},
		"execution": {"runtime", "launcher", "executor"}, "browser": {"runtime", "browser-proxy", "browser", "mcp"},
		"github": {"runtime", "egress", "github"}, "secrets": {"runtime"}, "coordination": {"runtime", "egress"}, "sharing": {"runtime", "launcher", "executor", "endpoints", "mcp"},
	}
	for _, service := range plan.Services {
		if service.Name == "endpoints" {
			roles["execution"] = append(roles["execution"], "endpoints")
			roles["browser"] = append(roles["browser"], "endpoints")
		}
	}
	for _, choice := range plan.Enabled {
		if choice.ID == "git" {
			for _, capability := range choice.Capabilities {
				if capability == "signing" {
					roles["git"] = append(roles["git"], "git-signing")
				}
			}
		}
	}
	probes := map[tools.ID]Probe{}
	for _, program := range plan.Programs {
		module := program.Module
		probes[module] = func(ctx context.Context, _ string) error {
			once.Do(func() {
				if backend == nil || reservation == nil {
					observationError = fmt.Errorf("selected full services are stopped; run loki tools start")
					return
				}
				observed, observationError = backend.Observe(ctx, *reservation)
				current, err := s.PlanFull()
				if err != nil || fullPlanDigest(current) != fullPlanDigest(plan) {
					observationError = fmt.Errorf("selection changed during doctor; retry with its current configuration")
				}
			})
			if observationError != nil {
				return observationError
			}
			for _, role := range roles[module] {
				if observed.Services[role] != "ready" {
					reason := observed.Services[role]
					if reason == "" {
						reason = "service has not been observed ready"
					}
					return fmt.Errorf("%s: %s", role, reason)
				}
			}
			return nil
		}
	}
	return probes, nil
}
