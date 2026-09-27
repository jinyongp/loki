package windows

import (
	"context"
	"errors"
	"fmt"
)

type PortProbe interface {
	Available(int) (bool, error)
}

type StaleApproval func(ExistingSnapshot) (bool, error)

type FreshInstaller interface {
	Install(context.Context, ExpectedInstallation, InstallOptions) error
}

type InstallController struct {
	Collector  PreflightCollector
	Recovery   RecoveryExecutor
	Port       PortProbe
	Fresh      FreshInstaller
	Filesystem StateFilesystem
}

type InstallDisposition string

const (
	InstallNoop      InstallDisposition = "already-healthy"
	InstallCompleted InstallDisposition = "installed"
)

type InstallResult struct {
	Disposition InstallDisposition
	Options     InstallOptions
	Snapshot    ExistingSnapshot
}

type InstallBlockedError struct {
	Reason string
}

func (err InstallBlockedError) Error() string {
	return "Windows Loki install blocked: " + err.Reason
}

func (controller InstallController) Run(
	ctx context.Context,
	expected ExpectedInstallation,
	options InstallOptions,
	approve StaleApproval,
) (InstallResult, error) {
	if controller.Port == nil || controller.Fresh == nil || controller.Filesystem == nil {
		return InstallResult{}, errors.New("Windows installer adapters are incomplete")
	}
	snapshot, err := controller.Collector.Collect(ctx, expected)
	if err != nil {
		return InstallResult{}, err
	}
	if snapshot.Windows.Present && !snapshot.Windows.Owned {
		return InstallResult{}, InstallBlockedError{Reason: "unverified-windows-state"}
	}
	if snapshot.StartupTask.Present && !snapshot.StartupTask.Owned {
		return InstallResult{}, InstallBlockedError{Reason: "unverified-startup-task"}
	}

	options.ApplyPreserved(snapshot.Windows)
	if options.InstallLocation != "" {
		locationInfo, statErr := controller.Filesystem.Lstat(options.InstallLocation)
		if statErr != nil {
			return InstallResult{}, statErr
		}
		if locationInfo.Exists {
			ownedRequestedLocation := snapshot.Windows.Kind == WindowsStateManifest &&
				snapshot.Windows.InstallLocation != "" &&
				WindowsPathEqual(options.InstallLocation, snapshot.Windows.InstallLocation)
			if !ownedRequestedLocation {
				return InstallResult{}, InstallBlockedError{Reason: "install-location-exists-unowned"}
			}
		}
	}
	if snapshot.Distribution.State == DistributionStale &&
		options.MCPPortExplicit && snapshot.Windows.MCPPort > 0 &&
		options.MCPPort != snapshot.Windows.MCPPort {
		available, probeErr := controller.Port.Available(options.MCPPort)
		if probeErr != nil {
			return InstallResult{}, probeErr
		}
		if !available {
			return InstallResult{}, InstallBlockedError{Reason: "requested-port-in-use"}
		}
	}

	assessment := AssessExistingInstallation(snapshot)
	switch assessment.Action {
	case ExistingBlocked:
		return InstallResult{}, InstallBlockedError{Reason: assessment.Reason}
	case ExistingHealthyNoop:
		return InstallResult{Disposition: InstallNoop, Options: options, Snapshot: snapshot}, nil
	case ExistingStaleNeedsApproval:
		approved := options.ReinstallRequested
		if !approved {
			if approve == nil {
				return InstallResult{}, InstallBlockedError{Reason: "stale-reinstall-approval-required"}
			}
			approved, err = approve(snapshot)
			if err != nil {
				return InstallResult{}, err
			}
		}
		if !approved {
			return InstallResult{}, InstallBlockedError{Reason: "stale-reinstall-cancelled"}
		}
		if _, err = controller.Recovery.Recover(ctx, expected, snapshot, true); err != nil {
			return InstallResult{}, err
		}
	case ExistingRemoveOrphan:
		if _, err = controller.Recovery.Recover(ctx, expected, snapshot, false); err != nil {
			return InstallResult{}, err
		}
	case ExistingFresh:
	default:
		return InstallResult{}, fmt.Errorf("unsupported installer action %q", assessment.Action)
	}

	if options.InstallLocation != "" {
		locationInfo, statErr := controller.Filesystem.Lstat(options.InstallLocation)
		if statErr != nil {
			return InstallResult{}, statErr
		}
		if locationInfo.Exists {
			return InstallResult{}, InstallBlockedError{Reason: "fresh-install-location-exists"}
		}
	}
	available, err := controller.Port.Available(options.MCPPort)
	if err != nil {
		return InstallResult{}, err
	}
	if !available {
		return InstallResult{}, InstallBlockedError{Reason: "mcp-port-in-use"}
	}
	if err := controller.Fresh.Install(ctx, expected, options); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Disposition: InstallCompleted, Options: options}, nil
}
