package windows

import (
	"context"
	"errors"
	"fmt"

	"loki/internal/progress"
)

type PreparedAppliance struct {
	Path string
}

type ConnectionMaterial struct {
	LocalOrigin        string
	Transport          string
	Reachability       string
	AuthenticationType string
	Token              string
}

type FreshInstallPlatform interface {
	PrepareAppliance(context.Context, InstallOptions) (PreparedAppliance, func(), error)
	RegisterDistribution(context.Context, ExpectedInstallation, InstallOptions, PreparedAppliance) (bool, error)
	Provision(context.Context, ExpectedInstallation, InstallOptions) (ConnectionMaterial, error)
	PublishWindowsState(context.Context, ExpectedInstallation, InstallOptions, ConnectionMaterial) (bool, error)
	CreateStartupTask(context.Context, ExpectedInstallation) (bool, error)
	PublishOwnership(context.Context, ExpectedInstallation, InstallOptions) error
	RollbackFresh(context.Context, ExpectedInstallation, InstallOptions, InstallTransaction) error
}

type TransactionalFreshInstaller struct {
	Platform FreshInstallPlatform
	Progress progress.Reporter
}

func (installer TransactionalFreshInstaller) Install(
	ctx context.Context,
	expected ExpectedInstallation,
	options InstallOptions,
) (resultErr error) {
	if installer.Platform == nil {
		return errors.New("fresh-install platform is unavailable")
	}
	progress.Emit(installer.Progress, progress.Event{Operation: "install", Phase: "prepare-appliance", State: progress.StateStarted, Message: "Preparing the verified WSL appliance image..."})
	appliance, cleanup, err := installer.Platform.PrepareAppliance(ctx, options)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	transaction := InstallTransaction{InstallLocation: options.InstallLocation}
	installationComplete := false
	defer func() {
		if installationComplete || !transaction.CreatedDistribution {
			return
		}
		progress.Emit(installer.Progress, progress.Event{Operation: "install", Phase: "rollback", State: progress.StateStarted, Message: "Rolling back the incomplete Windows installation..."})
		if rollbackErr := installer.Platform.RollbackFresh(ctx, expected, options, transaction); rollbackErr != nil {
			if resultErr == nil {
				resultErr = fmt.Errorf("rollback incomplete installation: %w", rollbackErr)
			} else {
				resultErr = fmt.Errorf("%w; rollback incomplete installation: %v", resultErr, rollbackErr)
			}
		}
	}()

	progress.Emit(installer.Progress, progress.Event{Operation: "install", Phase: "register", State: progress.StateStarted, Message: "Registering the Loki WSL distribution..."})
	createdDistribution, registerErr := installer.Platform.RegisterDistribution(ctx, expected, options, appliance)
	transaction.CreatedDistribution = createdDistribution
	if registerErr != nil {
		return registerErr
	}
	if !createdDistribution {
		return errors.New("fresh-install platform registered no owned distribution")
	}

	progress.Emit(installer.Progress, progress.Event{Operation: "install", Phase: "provision", State: progress.StateStarted, Message: "Provisioning Loki services inside WSL..."})
	connection, err := installer.Platform.Provision(ctx, expected, options)
	if err != nil {
		return err
	}
	progress.Emit(installer.Progress, progress.Event{Operation: "install", Phase: "publish-state", State: progress.StateStarted, Message: "Publishing the local MCP connection state..."})
	createdStateDir, stateErr := installer.Platform.PublishWindowsState(ctx, expected, options, connection)
	transaction.CreatedStateDir = createdStateDir
	if stateErr != nil {
		return stateErr
	}
	if !createdStateDir {
		return errors.New("fresh-install platform published no owned Windows state")
	}

	if options.AutoStart {
		progress.Emit(installer.Progress, progress.Event{Operation: "install", Phase: "startup", State: progress.StateStarted, Message: "Configuring Windows logon startup..."})
		createdTask, taskErr := installer.Platform.CreateStartupTask(ctx, expected)
		transaction.CreatedStartupTask = createdTask
		if taskErr != nil {
			return taskErr
		}
		if !createdTask {
			return errors.New("fresh-install platform created no owned startup task")
		}
	}
	progress.Emit(installer.Progress, progress.Event{Operation: "install", Phase: "ownership", State: progress.StateStarted, Message: "Recording Windows installation ownership..."})
	if err = installer.Platform.PublishOwnership(ctx, expected, options); err != nil {
		return err
	}
	installationComplete = true
	return nil
}
