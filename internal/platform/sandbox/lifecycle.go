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
	if e == nil || !resource.Valid() {
		return CleanupFailed, errors.New("sandbox cleanup is not configured")
	}
	owned, err := e.inspectRef(context.Background(), version, resource.Name(), resource)
	if err != nil {
		return CleanupFailed, err
	}
	if !owned.state.Exists {
		if err := e.verifyAbsent(version, resource); err != nil {
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
		stopErr := e.stop(stopCtx, version, resource)
		cancel()

		postStop, inspectErr := e.inspectRef(context.Background(), version, resource.Name(), resource)
		if inspectErr != nil {
			return CleanupFailed, errors.Join(stopErr, inspectErr)
		}
		if !postStop.state.Exists {
			if err := e.verifyAbsent(version, resource); err != nil {
				return CleanupFailed, err
			}
			return CleanupComplete, nil
		}
		if postStop.id != ownedID {
			return CleanupFailed, errors.Join(stopErr, errors.New("sandbox resource identity changed during cleanup"))
		}
		if postStop.state.Running {
			killCtx, killCancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
			killErr := e.kill(killCtx, version, resource)
			killCancel()
			if killErr != nil {
				return CleanupFailed, errors.Join(stopErr, killErr)
			}
			postKill, inspectErr := e.inspectRef(context.Background(), version, resource.Name(), resource)
			if inspectErr != nil {
				return CleanupFailed, inspectErr
			}
			if !postKill.state.Exists {
				if err := e.verifyAbsent(version, resource); err != nil {
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
	removeErr := e.remove(removeCtx, version, resource)
	cancel()
	if removeErr != nil {
		return CleanupFailed, removeErr
	}
	if err := e.verifyAbsent(version, resource); err != nil {
		return CleanupFailed, err
	}
	return CleanupComplete, nil
}

func (e *Engine) verifyAbsent(version string, resource Resource) error {
	ctx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
	defer cancel()
	for {
		state, err := e.inspect(ctx, version, resource)
		if err != nil {
			return err
		}
		if !state.Exists {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("sandbox resource remained after cleanup")
		case <-time.After(cleanupPollInterval):
		}
	}
}
