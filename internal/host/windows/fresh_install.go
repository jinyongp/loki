package windows

import (
	"context"
	"errors"
	"fmt"
)

type PreparedAppliance struct {
	Path string
}

type ConnectionMaterial struct {
	LocalOrigin  string
	Transport    string
	Reachability string
	Token        string
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
}

func (installer TransactionalFreshInstaller) Install(
	ctx context.Context,
	expected ExpectedInstallation,
	options InstallOptions,
) (resultErr error) {
	if installer.Platform == nil {
		return errors.New("fresh-install platform is unavailable")
	}
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
		if rollbackErr := installer.Platform.RollbackFresh(ctx, expected, options, transaction); rollbackErr != nil {
			if resultErr == nil {
				resultErr = fmt.Errorf("rollback incomplete installation: %w", rollbackErr)
			} else {
				resultErr = fmt.Errorf("%w; rollback incomplete installation: %v", resultErr, rollbackErr)
			}
		}
	}()

	createdDistribution, registerErr := installer.Platform.RegisterDistribution(ctx, expected, options, appliance)
	transaction.CreatedDistribution = createdDistribution
	if registerErr != nil {
		return registerErr
	}
	if !createdDistribution {
		return errors.New("fresh-install platform registered no owned distribution")
	}

	connection, err := installer.Platform.Provision(ctx, expected, options)
	if err != nil {
		return err
	}
	createdStateDir, stateErr := installer.Platform.PublishWindowsState(ctx, expected, options, connection)
	transaction.CreatedStateDir = createdStateDir
	if stateErr != nil {
		return stateErr
	}
	if !createdStateDir {
		return errors.New("fresh-install platform published no owned Windows state")
	}

	if options.AutoStart {
		createdTask, taskErr := installer.Platform.CreateStartupTask(ctx, expected)
		transaction.CreatedStartupTask = createdTask
		if taskErr != nil {
			return taskErr
		}
		if !createdTask {
			return errors.New("fresh-install platform created no owned startup task")
		}
	}
	if err = installer.Platform.PublishOwnership(ctx, expected, options); err != nil {
		return err
	}
	installationComplete = true
	return nil
}
