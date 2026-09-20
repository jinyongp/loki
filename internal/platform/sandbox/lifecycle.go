package sandbox

import (
	"context"
	"errors"
	"time"
)

const cleanupPollInterval = 20 * time.Millisecond

func outcomeForError(err error) Outcome {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return OutcomeTimedOut
	case errors.Is(err, context.Canceled):
		return OutcomeCanceled
	default:
		return OutcomeUnknown
	}
}

func (e *Engine) joinCleanup(version string, resource Resource, containerID string, primary error) (CleanupStatus, error) {
	status, cleanupErr := e.cleanupResource(version, resource, containerID)
	return status, errors.Join(primary, cleanupErr)
}

func (e *Engine) cleanupResource(version string, resource Resource, containerID string) (CleanupStatus, error) {
	return e.cleanupComponentResource(version, resource, containerID, resourceComponentWorkload)
}

func (e *Engine) cleanupComponentResource(
	version string, resource Resource, containerID, component string,
) (CleanupStatus, error) {
	if e == nil || !resource.Valid() {
		return CleanupFailed, errors.New("sandbox cleanup is not configured")
	}
	name := componentContainerName(resource, component)
	if name == "" {
		return CleanupFailed, errors.New("sandbox cleanup component is invalid")
	}
	owned, err := e.inspectComponentRef(context.Background(), version, name, resource, component)
	if err != nil {
		return CleanupFailed, err
	}
	if !owned.state.Exists {
		if err := e.verifyComponentAbsent(version, resource, component); err != nil {
			return CleanupFailed, err
		}
		return CleanupComplete, nil
	}
	if containerIDPattern.MatchString(containerID) && owned.id != containerID {
		return CleanupFailed, errors.New("sandbox resource identity changed before cleanup")
	}
	ownedID := owned.id

	if owned.state.Running {
		stopCtx, cancel := context.WithTimeout(context.Background(), e.gracefulStopTimeout+e.controlTimeout)
		stopErr := e.stopRef(stopCtx, version, ownedID, resource)
		cancel()

		postStop, inspectErr := e.inspectComponentRef(context.Background(), version, ownedID, resource, component)
		if inspectErr != nil {
			return CleanupFailed, errors.Join(stopErr, inspectErr)
		}
		if !postStop.state.Exists {
			if err := e.verifyComponentAbsent(version, resource, component); err != nil {
				return CleanupFailed, err
			}
			return CleanupComplete, nil
		}
		if postStop.id != ownedID {
			return CleanupFailed, errors.Join(stopErr, errors.New("sandbox resource identity changed during cleanup"))
		}
		if postStop.state.Running {
			killCtx, killCancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
			killErr := e.killRef(killCtx, version, ownedID, resource)
			killCancel()
			if killErr != nil {
				return CleanupFailed, errors.Join(stopErr, killErr)
			}
			postKill, inspectErr := e.inspectComponentRef(context.Background(), version, ownedID, resource, component)
			if inspectErr != nil {
				return CleanupFailed, inspectErr
			}
			if !postKill.state.Exists {
				if err := e.verifyComponentAbsent(version, resource, component); err != nil {
					return CleanupFailed, err
				}
				return CleanupComplete, nil
			}
			if postKill.id != ownedID {
				return CleanupFailed, errors.New("sandbox resource identity changed during cleanup")
			}
		}
	}

	removeCtx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
	removeErr := e.removeRef(removeCtx, version, ownedID, resource)
	cancel()
	if removeErr != nil {
		return CleanupFailed, removeErr
	}
	if err := e.verifyComponentAbsent(version, resource, component); err != nil {
		return CleanupFailed, err
	}
	return CleanupComplete, nil
}

func (e *Engine) verifyAbsent(version string, resource Resource) error {
	return e.verifyComponentAbsent(version, resource, resourceComponentWorkload)
}

func (e *Engine) verifyComponentAbsent(version string, resource Resource, component string) error {
	name := componentContainerName(resource, component)
	if name == "" {
		return errors.New("sandbox cleanup component is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
	defer cancel()
	for {
		inspected, err := e.inspectComponentRef(ctx, version, name, resource, component)
		if err != nil {
			return err
		}
		if !inspected.state.Exists {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("sandbox resource remained after cleanup")
		case <-time.After(cleanupPollInterval):
		}
	}
}
