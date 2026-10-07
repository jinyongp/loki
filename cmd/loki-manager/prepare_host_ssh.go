package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"loki/internal/management"
)

func prepareSSHHost(ctx context.Context, frontend management.Store, selected management.ExecutionSelection, out, diagnostics io.Writer) error {
	if err := requireHostConnectionsDetached(frontend); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	probe := selected
	probe.Command = "/bin/sh"
	relay, err := management.RelaySelection(ctx, probe, []string{"-c", "uname -s; uname -m; printf '%s\\n' \"$HOME\""})
	if err != nil {
		return err
	}
	var response githubRelayOutput
	relay.Stdout, relay.Stderr = &response, diagnostics
	if err := relay.Run(); err != nil {
		return fmt.Errorf("SSH host cannot be reached with your configured SSH authentication: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(response.String()), "\n")
	if response.overflow || len(lines) != 3 || lines[0] != "Linux" || !strings.HasPrefix(lines[2], "/") || strings.ContainsAny(lines[2], "\x00\r") {
		return fmt.Errorf("automatic SSH preparation requires a Linux host with an absolute home directory")
	}
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[lines[1]]
	if arch == "" {
		return fmt.Errorf("SSH host requires Linux amd64 or arm64")
	}
	asset := fmt.Sprintf("loki-manager-%s-linux-%s.zip", management.ManagerRelease, arch)
	base := "https://github.com/jinyongp/loki/releases/download/v" + management.ManagerRelease + "/"
	fmt.Fprintln(diagnostics, "Downloading and verifying the SSH host manager...")
	client := defaultUpgradeDependencies().client
	sums, err := upgradeDownload(ctx, client, base+"SHA256SUMS", 2<<20)
	if err != nil {
		return err
	}
	archive, err := upgradeDownload(ctx, client, base+asset, managerDownloadLimit)
	if err != nil {
		return err
	}
	binary, err := verifiedManagerBinary(archive, sums, asset, "loki")
	if err != nil {
		return err
	}
	defer clear(binary)
	if selected.Root == "" {
		selected.Root = path.Join(lines[2], ".config", "loki")
	}
	bin := path.Join(lines[2], ".local", "lib", "loki", "bin")
	script := "set -eu; umask 077; d=$(mktemp -d /tmp/loki-manager.XXXXXXXX); trap 'rm -rf \"$d\"' EXIT; cat > \"$d/loki\"; chmod 0755 \"$d/loki\"; test \"$(\"$d/loki\" version)\" = \"loki $1\"; \"$d/loki\" --root \"$2\" install --bin-dir \"$3\""
	relay, err = management.RelaySelection(ctx, probe, []string{"-c", script, "loki", management.ManagerRelease, selected.Root, bin})
	if err != nil {
		return err
	}
	relay.Stdin, relay.Stdout, relay.Stderr = bytes.NewReader(binary), diagnostics, diagnostics
	if err := relay.Run(); err != nil {
		return fmt.Errorf("SSH manager preparation interrupted; retry hosts prepare: %w", err)
	}
	selected.Command = path.Join(bin, "loki")
	if err := frontend.SelectExecutionHost(selected); err != nil {
		return err
	}
	return success(out, "SSH manager prepared and selected. Run loki setup to select its tools.", map[string]any{"host": selected.Host})
}
