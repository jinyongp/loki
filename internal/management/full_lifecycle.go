package management

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type FullObservation struct {
	State    string            `json:"state"`
	Ready    bool              `json:"ready"`
	Services map[string]string `json:"services"`
}

// FullBackend is an owned service publisher. Prepare and Start must be
// idempotent for a reservation, including partially created resources. Observe
// must establish actual required role/protocol readiness; a process or socket
// existing alone does not establish browser readiness.
type FullBackend interface {
	DeploymentObserver
	Prepare(context.Context, DeploymentReservation, FullTopology, FullLayouts) error
	Start(context.Context, DeploymentReservation) error
	Observe(context.Context, DeploymentReservation) (FullObservation, error)
	Stop(context.Context, DeploymentReservation) error
}

type FullLifecycleReport struct {
	Deployment  *DeploymentReservation `json:"deployment,omitempty"`
	Observation FullObservation        `json:"observation"`
}

func (s Store) deploymentLock() (func(), error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(s.Root, "deployment-mutation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	unlock, err := lockFile(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("full lifecycle operation is already running: %w", err)
	}
	return func() { unlock(); _ = file.Close() }, nil
}

func (s Store) stopDeployment(ctx context.Context, backend FullBackend, reservation DeploymentReservation) error {
	if err := backend.Stop(ctx, reservation); err != nil {
		return err
	}
	return s.ReleaseDeployment(ctx, reservation.ID, backend)
}

// ReconcileFull keeps desired selection independent of actual services. Cached
// calls already consult activation; changed capabilities require new worker
// layouts. Old generations remain reserved through verified shutdown. An
// interrupted start resumes its existing reservation instead of leaking one.
func (s Store) ReconcileFull(ctx context.Context, backend FullBackend) (FullLifecycleReport, error) {
	result := FullLifecycleReport{Observation: FullObservation{State: "stopped", Ready: false, Services: map[string]string{}}}
	if backend == nil {
		return result, fmt.Errorf("full lifecycle requires its owned backend")
	}
	unlock, err := s.deploymentLock()
	if err != nil {
		return result, err
	}
	defer unlock()
	topology, err := s.FullTopology()
	if err != nil {
		return result, err
	}
	layouts, err := topology.Resources.Layouts()
	if err != nil {
		return result, err
	}
	reservations, err := s.Deployments()
	if err != nil {
		return result, err
	}
	digest := fullPlanDigest(topology.Resources.Plan)
	var matching *DeploymentReservation
	for _, reservation := range reservations {
		if len(topology.Services) != 0 && matching == nil && reservation.PlanDigest == digest {
			copy := reservation
			matching = &copy
			continue
		}
		if err := s.stopDeployment(ctx, backend, reservation); err != nil {
			return result, fmt.Errorf("previous deployment could not stop: %w", err)
		}
	}
	if len(topology.Services) == 0 {
		return result, nil
	}
	if matching != nil {
		observed, err := backend.Observe(ctx, *matching)
		if err == nil && observed.Ready {
			current, planErr := s.PlanFull()
			if planErr != nil || fullPlanDigest(current) != digest {
				return result, fmt.Errorf("tool selection changed during readiness observation; retry start")
			}
			result.Deployment, result.Observation = matching, observed
			return result, nil
		}
	} else {
		reservation, err := s.ReserveDeployment(topology.Resources.Plan)
		if err != nil {
			return result, err
		}
		matching = &reservation
	}
	result.Deployment = matching
	if err := backend.Prepare(ctx, *matching, topology, layouts); err != nil {
		return result, fmt.Errorf("deployment preparation interrupted; retry start to resume %s: %w", matching.ID, err)
	}
	if err := backend.Start(ctx, *matching); err != nil {
		return result, fmt.Errorf("deployment start interrupted; retry start to resume %s: %w", matching.ID, err)
	}
	result.Observation, err = backend.Observe(ctx, *matching)
	if err != nil {
		return result, err
	}
	current, err := s.PlanFull()
	if err != nil || fullPlanDigest(current) != digest {
		if stopErr := s.stopDeployment(ctx, backend, *matching); stopErr != nil {
			return result, fmt.Errorf("selection changed during start and shutdown is incomplete: %w", stopErr)
		}
		return result, fmt.Errorf("tool selection changed during deployment; retry with the current selection")
	}
	if !result.Observation.Ready {
		return result, fmt.Errorf("owned services are not ready; inspect tools doctor and retry start")
	}
	return result, nil
}

func (s Store) StopFull(ctx context.Context, backend FullBackend) error {
	if backend == nil {
		return fmt.Errorf("full stop requires its owned backend")
	}
	unlock, err := s.deploymentLock()
	if err != nil {
		return err
	}
	defer unlock()
	reservations, err := s.Deployments()
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if err := s.stopDeployment(ctx, backend, reservation); err != nil {
			return err
		}
	}
	return nil
}
