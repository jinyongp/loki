//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"loki/internal/progress"
)

type WindowsFreshPlatform struct {
	Binding  ReleaseBinding
	Client   *http.Client
	WSL      WSLClient
	Tasks    PowerShellStartupTaskSource
	Starter  ProcessStarter
	Sleep    SleepFunc
	Attempts int
	Runner   NativeRunner
	Progress progress.Reporter
}

func (platform WindowsFreshPlatform) PrepareAppliance(ctx context.Context, options InstallOptions) (PreparedAppliance, func(), error) {
	return ReleaseAppliancePreparer{
		Binding: platform.Binding, Client: platform.Client, Progress: platform.Progress,
	}.PrepareAppliance(ctx, options)
}

func (platform WindowsFreshPlatform) RegisterDistribution(
	ctx context.Context,
	expected ExpectedInstallation,
	options InstallOptions,
	appliance PreparedAppliance,
) (bool, error) {
	return platform.provisioner().RegisterDistribution(ctx, expected, options, appliance)
}

func (platform WindowsFreshPlatform) Provision(
	ctx context.Context,
	expected ExpectedInstallation,
	options InstallOptions,
) (ConnectionMaterial, error) {
	return platform.provisioner().Provision(ctx, expected, options)
}

func (platform WindowsFreshPlatform) provisioner() WSLFreshProvisioner {
	return WSLFreshProvisioner{
		Client: platform.WSL, Starter: platform.Starter,
		Sleep: platform.Sleep, Attempts: platform.Attempts, Progress: platform.Progress,
	}
}

func (platform WindowsFreshPlatform) PublishWindowsState(
	ctx context.Context,
	expected ExpectedInstallation,
	_ InstallOptions,
	material ConnectionMaterial,
) (bool, error) {
	connectionRaw, err := BuildPublicConnection(expected, material)
	if err != nil {
		return false, err
	}
	stateRoot := filepath.Dir(expected.StateDir)
	filesystem := OSStateFilesystem{}
	rootInfo, statErr := filesystem.Lstat(stateRoot)
	if statErr != nil {
		return false, fmt.Errorf("inspect Loki Windows state root: %w", statErr)
	}
	if !rootInfo.Exists {
		if err = os.Mkdir(stateRoot, 0o700); err != nil {
			return false, fmt.Errorf("create Loki Windows state root: %w", err)
		}
		rootInfo, statErr = filesystem.Lstat(stateRoot)
		if statErr != nil {
			return false, fmt.Errorf("inspect created Loki Windows state root: %w", statErr)
		}
	}
	if !rootInfo.Exists || !rootInfo.Directory || rootInfo.Reparse {
		return false, errors.New("Loki Windows state root is not a real directory")
	}
	if err = os.Mkdir(expected.StateDir, 0o700); err != nil {
		return false, fmt.Errorf("create Loki Windows state directory: %w", err)
	}
	created := true
	stateInfo, statErr := filesystem.Lstat(expected.StateDir)
	if statErr != nil {
		return created, fmt.Errorf("inspect created Loki Windows state directory: %w", statErr)
	}
	if !stateInfo.Exists || !stateInfo.Directory || stateInfo.Reparse {
		return created, errors.New("created Loki Windows state path is not a real directory")
	}
	if err = platform.protectDirectory(ctx, expected.StateDir); err != nil {
		return created, err
	}
	tokenPath := joinWindowsPath(expected.StateDir, "mcp-token")
	if err = writeExclusiveSynced(tokenPath, []byte(material.Token), 0o600); err != nil {
		return created, fmt.Errorf("write Windows MCP token: %w", err)
	}
	if err = platform.protectFile(ctx, tokenPath); err != nil {
		return created, err
	}
	connectionPath := joinWindowsPath(expected.StateDir, "connection.json")
	if err = writeExclusiveSynced(connectionPath, connectionRaw, 0o600); err != nil {
		return created, fmt.Errorf("write Windows MCP connection: %w", err)
	}
	return created, nil
}

func (platform WindowsFreshPlatform) CreateStartupTask(ctx context.Context, expected ExpectedInstallation) (bool, error) {
	return platform.Tasks.CreateOwned(ctx, expected)
}

func (platform WindowsFreshPlatform) PublishOwnership(
	ctx context.Context,
	expected ExpectedInstallation,
	options InstallOptions,
) error {
	raw, err := BuildOwnershipManifest(platform.Binding, expected, options)
	if err != nil {
		return err
	}
	path := joinWindowsPath(expected.StateDir, "ownership.json")
	if err = writeExclusiveSynced(path, raw, 0o600); err != nil {
		return fmt.Errorf("write Windows ownership manifest: %w", err)
	}
	return platform.protectFile(ctx, path)
}

func (platform WindowsFreshPlatform) RollbackFresh(
	ctx context.Context,
	expected ExpectedInstallation,
	_ InstallOptions,
	transaction InstallTransaction,
) error {
	var failures []error
	if transaction.CreatedStartupTask {
		if err := platform.Tasks.RemoveOwned(ctx, expected); err != nil {
			failures = append(failures, err)
		}
	}
	if transaction.CreatedStateDir {
		if err := removeCreatedStateDirectory(expected.StateDir); err != nil {
			failures = append(failures, fmt.Errorf("remove created Windows state: %w", err))
		}
	}
	unregistered := !transaction.CreatedDistribution
	if transaction.CreatedDistribution {
		if err := platform.WSL.Terminate(ctx, expected.Distribution); err != nil {
			failures = append(failures, err)
		}
		if err := platform.WSL.Unregister(ctx, expected.Distribution); err != nil {
			failures = append(failures, err)
		} else {
			unregistered = true
		}
	}
	if unregistered && transaction.InstallLocation != "" {
		if !SafeOwnedInstallLocation(transaction.InstallLocation, expected.StateDir) {
			failures = append(failures, errors.New("refusing to remove unsafe created WSL location"))
		} else if err := removeCreatedDirectoryTree(transaction.InstallLocation); err != nil {
			failures = append(failures, fmt.Errorf("remove created WSL location: %w", err))
		}
	}
	return errors.Join(failures...)
}

func (platform WindowsFreshPlatform) protectDirectory(ctx context.Context, target string) error {
	return applyPrivateACL(ctx, target, true)
}

func (platform WindowsFreshPlatform) protectFile(ctx context.Context, target string) error {
	return applyPrivateACL(ctx, target, false)
}

func writeExclusiveSynced(path string, raw []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err != nil {
		file.Close()
		_ = os.Remove(path)
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		_ = os.Remove(path)
		return syncErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return closeErr
	}
	return nil
}

func removeCreatedStateDirectory(target string) error {
	filesystem := OSStateFilesystem{}
	info, err := filesystem.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Exists {
		return nil
	}
	if !info.Directory || info.Reparse {
		return errors.New("created path is no longer a real directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"connection.json": true, "mcp-token": true, "ownership.json": true, "loki-keepalive.exe": true, "keepalive.sha256": true}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("created Windows state contains unexpected entry %q", entry.Name())
		}
		entryPath := joinWindowsPath(target, entry.Name())
		entryInfo, statErr := filesystem.Lstat(entryPath)
		if statErr != nil {
			return statErr
		}
		if !entryInfo.Exists || !entryInfo.Regular || entryInfo.Reparse {
			return fmt.Errorf("created Windows state entry %q changed type", entry.Name())
		}
		if err := os.Remove(entryPath); err != nil {
			return fmt.Errorf("remove created Windows state entry %q: %w", entry.Name(), err)
		}
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("remove created Windows state directory: %w", err)
	}
	return nil
}

func removeCreatedDirectoryTree(target string) error {
	filesystem := OSStateFilesystem{}
	info, err := filesystem.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Exists {
		return nil
	}
	if !info.Directory || info.Reparse {
		return errors.New("created path is no longer a real directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entryPath := filepath.Join(target, entry.Name())
		entryInfo, statErr := filesystem.Lstat(entryPath)
		if statErr != nil {
			return statErr
		}
		if !entryInfo.Exists || entryInfo.Reparse {
			return fmt.Errorf("created directory entry %q changed type", entry.Name())
		}
		if entryInfo.Directory {
			if err := removeCreatedDirectoryTree(entryPath); err != nil {
				return err
			}
			continue
		}
		if !entryInfo.Regular {
			return fmt.Errorf("created directory entry %q is not a regular file", entry.Name())
		}
		if err := os.Remove(entryPath); err != nil {
			return fmt.Errorf("remove created directory entry %q: %w", entry.Name(), err)
		}
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("remove created directory: %w", err)
	}
	return nil
}

func NewWindowsInstallController(binding ReleaseBinding) InstallController {
	return NewWindowsInstallControllerWithProgress(binding, nil)
}

func NewWindowsInstallControllerWithProgress(
	binding ReleaseBinding,
	reporter progress.Reporter,
) InstallController {
	runner := ExecNativeRunner{}
	wsl := WSLClient{Runner: runner}
	tasks := PowerShellStartupTaskSource{}
	filesystem := OSStateFilesystem{}
	freshPlatform := WindowsFreshPlatform{
		Binding:  binding,
		WSL:      wsl,
		Tasks:    tasks,
		Runner:   runner,
		Progress: reporter,
	}
	collector := PreflightCollector{Filesystem: filesystem, Tasks: tasks, WSL: wsl}
	return InstallController{
		Collector: collector,
		Recovery: RecoveryExecutor{
			Collector:  collector,
			Tasks:      tasks,
			Filesystem: filesystem,
			Remover:    OSPathRemover{},
		},
		Port:       LoopbackPortProbe{},
		Fresh:      TransactionalFreshInstaller{Platform: freshPlatform, Progress: reporter},
		Filesystem: filesystem,
	}
}
