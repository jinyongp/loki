//go:build windows

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	windowshost "loki/internal/host/windows"
	"loki/internal/management"
	"loki/internal/progress"
	"loki/internal/tools"
)

func prepareManagedHost(ctx context.Context, frontend management.Store, diagnostics io.Writer) (*management.ExecutionSelection, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	reporter := progress.NewLineReporter(diagnostics)
	stop := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{Operation: "setup", Phase: "host", Message: "Still preparing the managed WSL host"})
	defer stop()
	client := windowshost.WSLClient{Runner: windowshost.ExecNativeRunner{}}
	if err := client.RequireInstallCapabilities(ctx); err != nil {
		return nil, fmt.Errorf("WSL needs installation or an update; run 'wsl --install --no-distribution' in an administrator terminal, restart Windows if requested, then retry loki setup: %w", err)
	}
	const distribution = "loki-tools"
	record, err := frontend.HostPreparation()
	if err != nil {
		return nil, err
	}
	present, err := client.DistributionPresent(ctx, distribution)
	if err != nil {
		return nil, err
	}
	runner := windowshost.ExecNativeRunner{}
	wsl := func(args ...string) (string, error) {
		p, err := runner.RunStreaming(ctx, "wsl.exe", args, reporter)
		if err != nil {
			return "", err
		}
		if p.ExitCode != 0 {
			return "", fmt.Errorf("WSL host preparation failed (%d): %s %s; retry loki setup", p.ExitCode, p.Stdout, p.Stderr)
		}
		return strings.TrimSpace(p.Stdout), nil
	}
	rootArgs := []string{"--distribution", distribution, "--user", "root", "--exec"}
	if present {
		if record == nil {
			return nil, fmt.Errorf("WSL distribution %q already exists without Loki ownership; select another prepared host with loki hosts use", distribution)
		}
		marker, err := wsl(append(rootArgs, "/usr/bin/cat", "/var/lib/loki-host-owner")...)
		if err != nil || marker != record.Token {
			return nil, fmt.Errorf("managed WSL ownership could not be verified; existing distribution and data were retained")
		}
	}
	// Fetch and verify the Linux executable before creating or modifying a host.
	asset := "loki-manager-" + management.ManagerRelease + "-linux-" + runtime.GOARCH + ".zip"
	base := "https://github.com/jinyongp/loki/releases/download/v" + management.ManagerRelease + "/"
	fmt.Fprintln(diagnostics, "Downloading and verifying the Linux manager...")
	httpClient := defaultUpgradeDependencies().client
	sums, err := upgradeDownload(ctx, httpClient, base+"SHA256SUMS", 2<<20)
	if err != nil {
		return nil, err
	}
	archive, err := upgradeDownload(ctx, httpClient, base+asset, managerDownloadLimit)
	if err != nil {
		return nil, err
	}
	binary, err := verifiedManagerBinary(archive, sums, asset, "loki")
	if err != nil {
		return nil, err
	}
	if !present {
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, err
		}
		record = &management.HostPreparation{Schema: 1, Distribution: distribution, Token: hex.EncodeToString(token[:]), Phase: "reserved"}
		if err := frontend.SaveHostPreparation(*record); err != nil {
			return nil, err
		}
		fmt.Fprintln(diagnostics, "Installing a dedicated Ubuntu WSL host (existing distributions are retained)...")
		// WSL acquires Ubuntu from its official distribution catalog. No Loki
		// runtime or user-selected tool is preinstalled in this environment.
		if _, err := wsl("--install", "Ubuntu-24.04", "--name", distribution, "--no-launch", "--version", "2"); err != nil {
			return nil, err
		}
		mark := "set -eu; umask 077; test ! -e /var/lib/loki-host-owner; printf '%s\\n' \"$1\" > /var/lib/loki-host-owner"
		if _, err := wsl(append(rootArgs, "/bin/sh", "-c", mark, "loki", record.Token)...); err != nil {
			return nil, err
		}
		record.Phase = "registered"
		if err := frontend.SaveHostPreparation(*record); err != nil {
			return nil, err
		}
	}
	// Stream the already verified binary into an exclusive staging path. Only
	// the exact owned target is replaced; the public installer is never piped
	// into an administrator shell on the execution host.
	bootstrap := "set -eu; umask 077; d=$(mktemp -d /tmp/loki-manager.XXXXXXXX); trap 'rm -rf \"$d\"' EXIT; cat > \"$d/loki\"; chmod 0755 \"$d/loki\"; test \"$(\"$d/loki\" version)\" = \"loki $1\"; \"$d/loki\" --root /var/lib/loki-tools install --bin-dir /usr/local/bin"
	args := append(rootArgs, "/bin/sh", "-c", bootstrap, "loki", management.ManagerRelease)
	p, err := runner.RunInputStreaming(ctx, "wsl.exe", args, binary, reporter)
	clear(binary)
	if err != nil {
		return nil, err
	}
	if p.ExitCode != 0 {
		return nil, fmt.Errorf("installing the Linux manager failed: %s %s; retry loki setup", p.Stdout, p.Stderr)
	}
	selected := &management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "wsl", Distribution: distribution}, Root: "/var/lib/loki-tools", Command: "/usr/local/bin/loki", User: "root", Owned: true}
	// Check the installed command, not just the successful copy operation.
	relay, err := management.RelaySelection(ctx, *selected, []string{"version"})
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	relay.Stdout, relay.Stderr = &output, diagnostics
	if err := relay.Run(); err != nil || strings.TrimSpace(output.String()) != "loki "+management.ManagerRelease {
		return nil, fmt.Errorf("managed execution host did not report the expected manager version")
	}
	if err := windowshost.EnsureToolsStartup(ctx, frontend.Root, distribution); err != nil {
		return nil, err
	}
	record.Phase = "ready"
	if err := frontend.SaveHostPreparation(*record); err != nil {
		return nil, err
	}
	if err := frontend.SelectExecutionHost(*selected); err != nil {
		return nil, err
	}
	return selected, nil
}
