package main

import (
	"context"
	"fmt"
	"io"
	windowshost "loki/internal/host/windows"
	"loki/internal/management"
	"os"
	"path/filepath"
	"strings"
)

func removeManagedHost(ctx context.Context, frontend management.Store, selected management.ExecutionSelection, diagnostics io.Writer) error {
	record, err := frontend.HostPreparation()
	if err != nil {
		return err
	}
	if selected.Host.Kind != "wsl" || !selected.Owned || record == nil || selected.Host.Distribution != record.Distribution {
		return fmt.Errorf("only this installation's owned WSL host can be purged; use hosts remove to detach an external host")
	}
	client := windowshost.WSLClient{Runner: windowshost.ExecNativeRunner{}}
	present, err := client.DistributionPresent(ctx, record.Distribution)
	if err != nil {
		return err
	}
	if present {
		runner := windowshost.ExecNativeRunner{}
		result, err := runner.Run(ctx, "wsl.exe", []string{"--distribution", record.Distribution, "--user", "root", "--exec", "/usr/bin/cat", "/var/lib/loki-host-owner"})
		if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != record.Token {
			return fmt.Errorf("WSL ownership differs; preserving the distribution")
		}
	}
	// Stop and remove provider credentials and its restore task before its host.
	if err := removeHostConnections(ctx, frontend, selected, diagnostics); err != nil {
		return err
	}
	if err := windowshost.RemoveToolsStartup(ctx, frontend.Root, record.Distribution); err != nil {
		return err
	}
	if present {
		result, err := (windowshost.ExecNativeRunner{}).Run(ctx, "wsl.exe", []string{"--unregister", record.Distribution})
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("owned WSL removal interrupted; retry hosts remove --purge")
		}
	}
	path := filepath.Join(frontend.Root, "control", "hosts", record.Distribution)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := (windowshost.WindowsHelperInstallPlatform{}).VerifyPrivatePath(path, true); err != nil {
		return err
	}
	return os.RemoveAll(path)
}
