package windows

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

type PortProbe interface {
	Available(int) (bool, error)
}

type StaleApproval func(ExistingSnapshot) (bool, error)

type FreshInstaller interface {
	Install(context.Context, ExpectedInstallation, InstallOptions) error
}

type InstallController struct {
	Collector      PreflightCollector
	Recovery       RecoveryExecutor
	Port           PortProbe
	Fresh          FreshInstaller
	Filesystem     StateFilesystem
	DesiredVersion string
}

type InstallDisposition string

const (
	InstallNoop            InstallDisposition = "already-healthy"
	InstallUpgradeRequired InstallDisposition = "upgrade-required"
	InstallCompleted       InstallDisposition = "installed"
)

type InstallResult struct {
	Disposition    InstallDisposition
	Options        InstallOptions
	Snapshot       ExistingSnapshot
	CurrentVersion string
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
	if err := controller.Collector.WSL.RequireInstallCapabilities(ctx); err != nil {
		return InstallResult{}, err
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
	if assessment.Action == ExistingRemoveOrphan {
		available, probeErr := controller.Port.Available(options.MCPPort)
		if probeErr != nil {
			return InstallResult{}, probeErr
		}
		if !available {
			return InstallResult{}, InstallBlockedError{Reason: "mcp-port-in-use"}
		}
	}
	switch assessment.Action {
	case ExistingBlocked:
		return InstallResult{}, InstallBlockedError{Reason: assessment.Reason}
	case ExistingHealthyNoop:
		currentVersion := snapshot.Distribution.Version
		if strings.TrimSpace(controller.DesiredVersion) != "" {
			managedVersion, managedErr := controller.Collector.WSL.ManagedReleaseVersion(ctx, expected.Distribution)
			if managedErr == nil {
				currentVersion = managedVersion
			} else if strings.TrimSpace(currentVersion) == strings.TrimSpace(controller.DesiredVersion) {
				return InstallResult{}, InstallBlockedError{Reason: "managed-release-unverifiable"}
			}
		}
		disposition, versionErr := controller.healthyDisposition(currentVersion)
		if versionErr != nil {
			return InstallResult{}, versionErr
		}
		return InstallResult{
			Disposition: disposition, Options: options, Snapshot: snapshot, CurrentVersion: currentVersion,
		}, nil
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

func (controller InstallController) healthyDisposition(currentVersion string) (InstallDisposition, error) {
	desired := strings.TrimSpace(controller.DesiredVersion)
	current := strings.TrimSpace(currentVersion)
	if desired == "" || current == desired {
		return InstallNoop, nil
	}
	currentSemver := "v" + strings.TrimPrefix(current, "v")
	desiredSemver := "v" + strings.TrimPrefix(desired, "v")
	if !semver.IsValid(currentSemver) || !semver.IsValid(desiredSemver) {
		return "", errors.New("Windows Loki appliance release version is invalid")
	}
	switch semver.Compare(currentSemver, desiredSemver) {
	case -1:
		return InstallUpgradeRequired, nil
	case 0:
		return InstallNoop, nil
	default:
		return "", InstallBlockedError{Reason: "appliance-newer-than-frontend"}
	}
}
