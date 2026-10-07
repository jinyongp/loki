//go:build windows

package windows

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Reuse the console-free keepalive and scheduled-task ownership checks for the
// modular host. Its transport files never overlap the legacy appliance state.
func EnsureToolsStartup(ctx context.Context, root, distribution string) error {
	if err := ValidateDistributionName(distribution); err != nil {
		return err
	}
	expected, err := ExpectedFromOptions(InstallOptions{Distribution: distribution}, os.Getenv("LOCALAPPDATA"), os.Getenv("SystemRoot"))
	if err != nil {
		return err
	}
	expected.StateDir = filepath.Join(root, "control", "hosts", distribution)
	expected.TaskExecutable = filepath.Join(expected.StateDir, "loki-keepalive.exe")
	expected.TaskName = "Loki tools WSL (" + distribution + ")"
	if err := validateExistingDirectoryPrefixes(expected.StateDir); err != nil {
		return err
	}
	if err := os.MkdirAll(expected.StateDir, 0700); err != nil {
		return err
	}
	if err := (WindowsFreshPlatform{}).protectDirectory(ctx, expected.StateDir); err != nil {
		return err
	}
	tasks := PowerShellStartupTaskSource{}
	probe, err := tasks.Probe(ctx, expected.TaskName)
	if err != nil {
		return err
	}
	if !probe.Present {
		if _, err := tasks.CreateOwned(ctx, expected); err != nil {
			return err
		}
	} else if !ClassifyStartupTask(probe, expected).Owned {
		return fmt.Errorf("refusing to change an unowned startup task %q", expected.TaskName)
	}
	return NewWindowsConnectionStartupPlatform(os.Getenv("LOCALAPPDATA")).StartKeepalive(ctx, expected)
}

func RemoveToolsStartup(ctx context.Context, root, distribution string) error {
	if err := ValidateDistributionName(distribution); err != nil {
		return err
	}
	expected, err := ExpectedFromOptions(InstallOptions{Distribution: distribution}, os.Getenv("LOCALAPPDATA"), os.Getenv("SystemRoot"))
	if err != nil {
		return err
	}
	expected.StateDir = filepath.Join(root, "control", "hosts", distribution)
	expected.TaskExecutable = filepath.Join(expected.StateDir, "loki-keepalive.exe")
	expected.TaskName = "Loki tools WSL (" + distribution + ")"
	return (PowerShellStartupTaskSource{}).RemoveOwned(ctx, expected)
}
