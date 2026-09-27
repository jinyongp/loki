package windows

import (
	"context"
	"errors"
	"fmt"
)

type UninstallPlatform interface {
	Collect(context.Context, ExpectedInstallation) (ExistingSnapshot, error)
	RemoveConnections(context.Context, string) error
	RemoveStartupTask(context.Context, ExpectedInstallation) error
	VerifyDistributionIdentity(context.Context, string, string) error
	TerminateDistribution(context.Context, string) error
	UnregisterDistribution(context.Context, string) error
	DistributionPresent(context.Context, string) (bool, error)
	RemoveWindowsState(ExpectedInstallation, WindowsState) error
}

type UninstallController struct {
	Platform UninstallPlatform
}

func (controller UninstallController) Run(
	ctx context.Context,
	expected ExpectedInstallation,
	approved bool,
) error {
	if !approved {
		return errors.New("Windows Loki uninstall requires explicit destructive approval")
	}
	if controller.Platform == nil {
		return errors.New("Windows uninstall platform is unavailable")
	}
	snapshot, err := controller.Platform.Collect(ctx, expected)
	if err != nil {
		return err
	}
	if err = validateUninstallSnapshot(snapshot); err != nil {
		return err
	}
	if uninstallSnapshotEmpty(snapshot) {
		return nil
	}

	live, err := controller.Platform.Collect(ctx, expected)
	if err != nil {
		return err
	}
	if !sameRecoverySnapshot(snapshot, live) {
		return errors.New("Loki uninstall state changed after approval; no destructive uninstall was performed")
	}
	if err = validateUninstallSnapshot(live); err != nil {
		return err
	}

	if err = controller.Platform.RemoveConnections(ctx, expected.Distribution); err != nil {
		return fmt.Errorf("remove Loki local connections: %w", err)
	}
	if live.StartupTask.Present {
		if err = controller.Platform.RemoveStartupTask(ctx, expected); err != nil {
			return err
		}
	}
	if live.Distribution.State != DistributionAbsent {
		if err = controller.Platform.VerifyDistributionIdentity(
			ctx, expected.Distribution, live.Distribution.Version,
		); err != nil {
			return err
		}
		if err = controller.Platform.TerminateDistribution(ctx, expected.Distribution); err != nil {
			return err
		}
		if err = controller.Platform.VerifyDistributionIdentity(
			ctx, expected.Distribution, live.Distribution.Version,
		); err != nil {
			return err
		}
		if err = controller.Platform.UnregisterDistribution(ctx, expected.Distribution); err != nil {
			return err
		}
		present, presentErr := controller.Platform.DistributionPresent(ctx, expected.Distribution)
		if presentErr != nil {
			return presentErr
		}
		if present {
			return fmt.Errorf("WSL still reports %q after uninstall unregister", expected.Distribution)
		}
	}
	if live.Windows.Present {
		if err = controller.Platform.RemoveWindowsState(expected, live.Windows); err != nil {
			return err
		}
	}
	final, err := controller.Platform.Collect(ctx, expected)
	if err != nil {
		return err
	}
	if !uninstallSnapshotEmpty(final) {
		return errors.New("Windows Loki uninstall did not converge to an absent installation")
	}
	return nil
}

func validateUninstallSnapshot(snapshot ExistingSnapshot) error {
	if snapshot.Windows.Present && !snapshot.Windows.Owned {
		return errors.New("refusing Windows Loki uninstall with unverified Windows state")
	}
	if snapshot.StartupTask.Present && !snapshot.StartupTask.Owned {
		return errors.New("refusing Windows Loki uninstall with unverified WSL startup task")
	}
	switch snapshot.Distribution.State {
	case DistributionAbsent:
	case DistributionHealthy, DistributionStale:
		if !snapshot.Windows.Present || !snapshot.Windows.Owned {
			return errors.New("refusing Windows Loki uninstall without verified per-distribution Windows ownership")
		}
	case DistributionProvisioning:
		return errors.New("refusing Windows Loki uninstall while appliance provisioning is in progress")
	case DistributionForeign, DistributionIndeterminate:
		return errors.New("refusing Windows Loki uninstall because WSL distribution ownership is not verified")
	default:
		return fmt.Errorf("refusing Windows Loki uninstall from unsupported distribution state %q", snapshot.Distribution.State)
	}
	return nil
}

func uninstallSnapshotEmpty(snapshot ExistingSnapshot) bool {
	return snapshot.Distribution.State == DistributionAbsent &&
		!snapshot.Windows.Present && !snapshot.StartupTask.Present
}

type LocalConnectionRemover interface {
	RemoveAllForDistribution(context.Context, string) error
}

type UninstallAdapter struct {
	Collector   PreflightCollector
	Tasks       StartupTaskManager
	Filesystem  StateFilesystem
	Remover     PathRemover
	Connections LocalConnectionRemover
}

func (adapter UninstallAdapter) Collect(ctx context.Context, expected ExpectedInstallation) (ExistingSnapshot, error) {
	return adapter.Collector.Collect(ctx, expected)
}

func (adapter UninstallAdapter) RemoveConnections(ctx context.Context, distribution string) error {
	if adapter.Connections == nil {
		return nil
	}
	return adapter.Connections.RemoveAllForDistribution(ctx, distribution)
}

func (adapter UninstallAdapter) RemoveStartupTask(ctx context.Context, expected ExpectedInstallation) error {
	if adapter.Tasks == nil {
		return errors.New("Windows startup task manager is unavailable")
	}
	return adapter.Tasks.RemoveOwned(ctx, expected)
}

func (adapter UninstallAdapter) VerifyDistributionIdentity(ctx context.Context, distribution, version string) error {
	return adapter.Collector.WSL.VerifyDistributionIdentity(ctx, distribution, version)
}

func (adapter UninstallAdapter) TerminateDistribution(ctx context.Context, distribution string) error {
	return adapter.Collector.WSL.Terminate(ctx, distribution)
}

func (adapter UninstallAdapter) UnregisterDistribution(ctx context.Context, distribution string) error {
	return adapter.Collector.WSL.Unregister(ctx, distribution)
}

func (adapter UninstallAdapter) DistributionPresent(ctx context.Context, distribution string) (bool, error) {
	return adapter.Collector.WSL.DistributionPresent(ctx, distribution)
}

func (adapter UninstallAdapter) RemoveWindowsState(expected ExpectedInstallation, state WindowsState) error {
	return RemoveOwnedWindowsState(adapter.Filesystem, adapter.Remover, expected, state)
}
