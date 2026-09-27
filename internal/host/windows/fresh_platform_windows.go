//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
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
}

func (platform WindowsFreshPlatform) PrepareAppliance(ctx context.Context, options InstallOptions) (PreparedAppliance, func(), error) {
	return ReleaseAppliancePreparer{Binding: platform.Binding, Client: platform.Client}.PrepareAppliance(ctx, options)
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
		Sleep: platform.Sleep, Attempts: platform.Attempts,
	}
}

func (platform WindowsFreshPlatform) PublishWindowsState(
	ctx context.Context,
	expected ExpectedInstallation,
	_ InstallOptions,
	material ConnectionMaterial,
) (bool, error) {
	if platform.Runner == nil {
		return false, errors.New("Windows native runner is unavailable")
	}
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
	current, err := user.Current()
	if err != nil {
		return created, fmt.Errorf("resolve current Windows user: %w", err)
	}
	if err = platform.protectDirectory(ctx, expected.StateDir, current.Username); err != nil {
		return created, err
	}
	tokenPath := joinWindowsPath(expected.StateDir, "mcp-token")
	if err = writeExclusiveSynced(tokenPath, []byte(material.Token), 0o600); err != nil {
		return created, fmt.Errorf("write Windows MCP token: %w", err)
	}
	if err = platform.protectFile(ctx, tokenPath, current.Username); err != nil {
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
	if platform.Runner == nil {
		return errors.New("Windows native runner is unavailable")
	}
	raw, err := BuildOwnershipManifest(platform.Binding, expected, options)
	if err != nil {
		return err
	}
	path := joinWindowsPath(expected.StateDir, "ownership.json")
	if err = writeExclusiveSynced(path, raw, 0o600); err != nil {
		return fmt.Errorf("write Windows ownership manifest: %w", err)
	}
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve current Windows user: %w", err)
	}
	return platform.protectFile(ctx, path, current.Username)
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
		} else if err := removeEmptyCreatedDirectory(transaction.InstallLocation); err != nil {
			failures = append(failures, fmt.Errorf("remove created WSL location: %w", err))
		}
	}
	return errors.Join(failures...)
}

func (platform WindowsFreshPlatform) protectDirectory(ctx context.Context, target, identity string) error {
	return platform.runACL(ctx, target,
		"/inheritance:r", "/grant:r", identity+":(OI)(CI)(F)", "SYSTEM:(OI)(CI)(F)")
}

func (platform WindowsFreshPlatform) protectFile(ctx context.Context, target, identity string) error {
	return platform.runACL(ctx, target,
		"/inheritance:r", "/grant:r", identity+":(F)", "SYSTEM:(F)")
}

func (platform WindowsFreshPlatform) runACL(ctx context.Context, target string, arguments ...string) error {
	all := append([]string{target}, arguments...)
	result, err := platform.Runner.Run(ctx, "icacls.exe", all)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return nativeFailure("protect Windows Loki state", result)
	}
	return nil
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
	allowed := map[string]bool{"connection.json": true, "mcp-token": true, "ownership.json": true}
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

func removeEmptyCreatedDirectory(target string) error {
	info, err := (OSStateFilesystem{}).Lstat(target)
	if err != nil {
		return err
	}
	if !info.Exists {
		return nil
	}
	if !info.Directory || info.Reparse {
		return errors.New("created path is no longer a real directory")
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("created directory is not empty or cannot be removed: %w", err)
	}
	return nil
}

func NewWindowsInstallController(binding ReleaseBinding) InstallController {
	runner := ExecNativeRunner{}
	wsl := WSLClient{Runner: runner}
	tasks := PowerShellStartupTaskSource{}
	filesystem := OSStateFilesystem{}
	freshPlatform := WindowsFreshPlatform{
		Binding: binding,
		WSL:     wsl,
		Tasks:   tasks,
		Runner:  runner,
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
		Fresh:      TransactionalFreshInstaller{Platform: freshPlatform},
		Filesystem: filesystem,
	}
}
