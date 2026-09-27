package windows

import (
	"context"
	"errors"
	"fmt"
)

type StartupTaskManager interface {
	StartupTaskSource
	RemoveOwned(context.Context, ExpectedInstallation) error
}

type PathRemover interface {
	RemoveAll(string) error
}

type RecoveryExecutor struct {
	Collector  PreflightCollector
	Tasks      StartupTaskManager
	Filesystem StateFilesystem
	Remover    PathRemover
}

func (executor RecoveryExecutor) Recover(
	ctx context.Context,
	expected ExpectedInstallation,
	snapshot ExistingSnapshot,
	staleApproved bool,
) (RecoveryPlan, error) {
	plan := BuildRecoveryPlan(snapshot, staleApproved)
	for _, step := range plan.Steps {
		switch step {
		case RecoveryRemoveStartupTask:
			if executor.Tasks == nil {
				return plan, errors.New("startup task manager is unavailable")
			}
			if err := executor.Tasks.RemoveOwned(ctx, expected); err != nil {
				return plan, err
			}
		case RecoveryTerminateDistro:
			if err := executor.Collector.WSL.Terminate(ctx, expected.Distribution); err != nil {
				return plan, err
			}
		case RecoveryUnregisterDistro:
			if err := executor.Collector.WSL.Unregister(ctx, expected.Distribution); err != nil {
				return plan, err
			}
		case RecoveryVerifyDistroGone:
			present, err := executor.Collector.WSL.DistributionPresent(ctx, expected.Distribution)
			if err != nil {
				return plan, err
			}
			if present {
				return plan, fmt.Errorf("WSL still reports %q after unregister", expected.Distribution)
			}
		case RecoveryRemoveWindowsState:
			if err := RemoveOwnedWindowsState(executor.Filesystem, executor.Remover, expected, snapshot.Windows); err != nil {
				return plan, err
			}
		case RecoveryVerifyFresh:
			fresh, err := executor.Collector.Collect(ctx, expected)
			if err != nil {
				return plan, err
			}
			if fresh.Distribution.State != DistributionAbsent || fresh.Windows.Present || fresh.StartupTask.Present {
				return plan, errors.New("Loki recovery did not converge to a clean fresh-install state")
			}
		default:
			return plan, fmt.Errorf("unsupported recovery step %q", step)
		}
	}
	return plan, nil
}

func RemoveOwnedWindowsState(
	filesystem StateFilesystem,
	remover PathRemover,
	expected ExpectedInstallation,
	state WindowsState,
) error {
	if !state.Present {
		return nil
	}
	if !state.Owned {
		return errors.New("refusing to remove unverified Windows state")
	}
	if filesystem == nil || remover == nil {
		return errors.New("Windows state removal adapters are unavailable")
	}
	stateInfo, err := filesystem.Lstat(expected.StateDir)
	if err != nil {
		return err
	}
	if !stateInfo.Exists {
		return nil
	}
	if !stateInfo.Directory || stateInfo.Reparse {
		return errors.New("refusing to remove Windows state because it is not a real directory")
	}
	if state.Kind == WindowsStateManifest && state.InstallLocation != "" {
		if !SafeOwnedInstallLocation(state.InstallLocation, expected.StateDir) {
			return errors.New("refusing to remove unsafe manifest-owned WSL location")
		}
		locationInfo, statErr := filesystem.Lstat(state.InstallLocation)
		if statErr != nil {
			return statErr
		}
		if locationInfo.Exists && (!locationInfo.Directory || locationInfo.Reparse) {
			return errors.New("refusing to remove manifest-owned WSL location because it is not a real directory")
		}
	}
	if err := remover.RemoveAll(expected.StateDir); err != nil {
		return fmt.Errorf("remove Windows state: %w", err)
	}
	if state.Kind == WindowsStateManifest && state.InstallLocation != "" {
		locationInfo, statErr := filesystem.Lstat(state.InstallLocation)
		if statErr != nil {
			return statErr
		}
		if locationInfo.Exists {
			if !locationInfo.Directory || locationInfo.Reparse {
				return errors.New("refusing to remove manifest-owned WSL location because it changed type")
			}
			if err := remover.RemoveAll(state.InstallLocation); err != nil {
				return fmt.Errorf("remove manifest-owned WSL location: %w", err)
			}
		}
	}
	return nil
}

type InstallTransaction struct {
	CreatedDistribution bool
	CreatedStateDir     bool
	CreatedStartupTask  bool
	InstallLocation     string
}

type RollbackStep string

const (
	RollbackRemoveStartupTask     RollbackStep = "remove-created-startup-task"
	RollbackRemoveStateDir        RollbackStep = "remove-created-state-dir"
	RollbackTerminateDistro       RollbackStep = "terminate-created-distribution"
	RollbackUnregisterDistro      RollbackStep = "unregister-created-distribution"
	RollbackRemoveInstallLocation RollbackStep = "remove-created-install-location"
)

func BuildRollbackPlan(transaction InstallTransaction) []RollbackStep {
	var steps []RollbackStep
	if transaction.CreatedStartupTask {
		steps = append(steps, RollbackRemoveStartupTask)
	}
	if transaction.CreatedStateDir {
		steps = append(steps, RollbackRemoveStateDir)
	}
	if transaction.CreatedDistribution {
		steps = append(steps, RollbackTerminateDistro, RollbackUnregisterDistro)
		if transaction.InstallLocation != "" {
			steps = append(steps, RollbackRemoveInstallLocation)
		}
	}
	return steps
}
