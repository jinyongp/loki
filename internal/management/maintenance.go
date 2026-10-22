package management

import (
	"context"
	"fmt"
	"io"
)

type fullInterruptionKey struct{}

// WithJobInterruption records explicit operator intent. The deployment's
// durable stop record freezes that choice before contacting the launcher.
func WithJobInterruption(ctx context.Context) context.Context {
	return context.WithValue(ctx, fullInterruptionKey{}, true)
}

func JobInterruptionRequested(ctx context.Context) bool {
	explicit, _ := ctx.Value(fullInterruptionKey{}).(bool)
	return explicit
}

type fullAdmissionFencer interface {
	FenceJobs(context.Context, DeploymentReservation) error
}

func CheckMaintenanceIdle(ctx context.Context, backend FullBackend) error {
	if JobInterruptionRequested(ctx) {
		if _, ok := backend.(fullAdmissionFencer); !ok {
			return fmt.Errorf("host does not provide exact Job interruption")
		}
		return nil
	}
	if idle, ok := backend.(interface{ RequireIdle(context.Context) error }); ok {
		return idle.RequireIdle(ctx)
	}
	return fmt.Errorf("host does not provide maintenance readiness")
}

// Caller owns Store.Lock. The same lock serializes service creation and
// selection publication, so a stopped old launcher cannot race a stale start.
func (s Store) pauseFullMutationLocked(ctx context.Context, deferServices bool) error {
	deployments, err := s.Deployments()
	if err != nil || len(deployments) == 0 {
		return err
	}
	backend := s.MaintenanceBackend
	if backend == nil {
		backend, err = NewFullBackend(s, io.Discard)
		if err != nil {
			return err
		}
	}
	for _, r := range deployments {
		if deferServices {
			guard, ok := backend.(fullAdmissionFencer)
			if !ok {
				return fmt.Errorf("host cannot fence Jobs before changing selection")
			}
			if err := guard.FenceJobs(ctx, r); err != nil {
				return err
			}
		} else if err := backend.Stop(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) runningArtifactsChange(candidate Snapshot) (bool, error) {
	deployments, err := s.Deployments()
	if err != nil {
		return false, err
	}
	for _, r := range deployments {
		for _, old := range r.Artifacts {
			current, exists := candidate.Installed[old.Module]
			if !exists || current.Artifact.Identity() != old.Identity() {
				return true, nil
			}
		}
	}
	return false, nil
}
