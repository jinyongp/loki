package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"loki/internal/management"
	"loki/internal/progress"
)

type remoteHostRunner func(context.Context, management.ExecutionSelection, []string, io.Reader, io.Writer, io.Writer) error

type remotePreparationDependencies struct {
	run      remoteHostRunner
	download func(context.Context, string) ([]byte, error)
}

func defaultRemotePreparationDependencies() remotePreparationDependencies {
	return remotePreparationDependencies{
		run: func(ctx context.Context, selected management.ExecutionSelection, args []string, input io.Reader, out, diagnostics io.Writer) error {
			command, err := management.RelaySelection(ctx, selected, args)
			if err != nil {
				return err
			}
			command.Stdin, command.Stdout, command.Stderr = input, out, diagnostics
			return command.Run()
		},
		download: func(ctx context.Context, arch string) ([]byte, error) {
			asset := fmt.Sprintf("loki-manager-%s-linux-%s.zip", management.ManagerRelease, arch)
			base := "https://github.com/jinyongp/loki/releases/download/v" + management.ManagerRelease + "/"
			client := defaultUpgradeDependencies().client
			sums, err := upgradeDownload(ctx, client, base+"SHA256SUMS", 2<<20)
			if err != nil {
				return nil, err
			}
			archive, err := upgradeDownload(ctx, client, base+asset, managerDownloadLimit)
			if err != nil {
				return nil, err
			}
			return verifiedManagerBinary(archive, sums, asset, "loki")
		},
	}
}

// User-managed hosts retain their ownership. Install only the selected user's
// private CLI, without replacing a different command found through PATH.
func prepareRemoteManager(ctx context.Context, selected management.ExecutionSelection, diagnostics io.Writer, deps remotePreparationDependencies) (management.ExecutionSelection, error) {
	prepared := selected
	err := prepareRemoteManagerInto(ctx, &prepared, diagnostics, deps)
	return prepared, err
}

func prepareRemoteManagerInto(ctx context.Context, target *management.ExecutionSelection, diagnostics io.Writer, deps remotePreparationDependencies) error {
	selected := *target
	if err := selected.Validate(); err != nil {
		return err
	}
	if (selected.Host.Kind != "wsl" && selected.Host.Kind != "ssh") || selected.Owned {
		return fmt.Errorf("remote manager preparation requires a user-managed WSL or SSH host")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	stop := progress.StartHeartbeat(ctx, progress.NewLineReporter(diagnostics), progress.HeartbeatOptions{Operation: "setup", Phase: "host", Message: "Still preparing the selected execution host"})
	defer stop()
	label := setupHostLabel(&selected)
	fmt.Fprintf(diagnostics, "Checking %s...\n", label)
	probe := selected
	probe.Command = "/bin/sh"
	var response githubRelayOutput
	probeCtx, probeCancel := context.WithTimeout(ctx, 30*time.Second)
	err := deps.run(probeCtx, probe, []string{"-c", "uname -s; uname -m; printf '%s\\n' \"$HOME\""}, nil, &response, diagnostics)
	probeCancel()
	if err != nil {
		return fmt.Errorf("cannot access %s; check host availability and login access: %w", label, err)
	}
	lines := strings.Split(strings.TrimSpace(response.String()), "\n")
	if response.overflow || len(lines) != 3 || lines[0] != "Linux" || !strings.HasPrefix(lines[2], "/") || strings.ContainsAny(lines[2], "\x00\r") {
		return fmt.Errorf("execution host preparation requires Linux with an absolute user home directory")
	}
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[lines[1]]
	if arch == "" {
		return fmt.Errorf("execution host preparation requires Linux amd64 or arm64")
	}
	if selected.Root == "" {
		selected.Root = path.Join(lines[2], ".config", "loki")
	}
	checkVersion := func(candidate management.ExecutionSelection) bool {
		versionCtx, versionCancel := context.WithTimeout(ctx, 30*time.Second)
		defer versionCancel()
		var version githubRelayOutput
		if deps.run(versionCtx, candidate, []string{"version"}, nil, &version, io.Discard) != nil || version.overflow {
			return false
		}
		return strings.TrimSpace(version.String()) == "loki "+management.ManagerRelease
	}
	if !checkVersion(selected) {
		if err := ctx.Err(); err != nil {
			return err
		}
		bin := path.Join(lines[2], ".local", "lib", "loki", "bin")
		selected.Command = path.Join(bin, "loki")
		// A previous interrupted setup may already have published this manager.
		if !checkVersion(selected) {
			fmt.Fprintf(diagnostics, "Preparing Loki %s on %s...\n", management.ManagerRelease, label)
			fmt.Fprintln(diagnostics, "Downloading and verifying the Linux manager...")
			binary, err := deps.download(ctx, arch)
			if err != nil {
				return err
			}
			defer clear(binary)
			script := "set -eu; umask 077; d=$(mktemp -d /tmp/loki-manager.XXXXXXXX); trap 'rm -rf \"$d\"' EXIT; cat > \"$d/loki\"; chmod 0755 \"$d/loki\"; test \"$(\"$d/loki\" version)\" = \"loki $1\"; \"$d/loki\" --root \"$2\" install --bin-dir \"$3\""
			var installation githubRelayOutput
			if err := deps.run(ctx, probe, []string{"-c", script, "loki", management.ManagerRelease, selected.Root, bin}, bytes.NewReader(binary), &installation, &installation); err != nil {
				return fmt.Errorf("installing the execution-host manager failed; retry loki setup: %w\n%s", err, strings.TrimSpace(installation.String()))
			}
			if !checkVersion(selected) {
				return fmt.Errorf("installed execution-host manager did not report the expected version; retry loki setup")
			}
		}
	}
	*target = selected
	return nil
}

func prepareUserManagedHost(ctx context.Context, frontend management.Store, selected management.ExecutionSelection, out, diagnostics io.Writer) error {
	return selectPreparedUserHost(ctx, frontend, selected, out, diagnostics, defaultRemotePreparationDependencies())
}

func selectPreparedUserHost(ctx context.Context, frontend management.Store, selected management.ExecutionSelection, out, diagnostics io.Writer, deps remotePreparationDependencies) error {
	prepared, err := prepareRemoteManager(ctx, selected, diagnostics, deps)
	if err != nil {
		return err
	}
	if err := frontend.SelectExecutionHost(prepared); err != nil {
		return err
	}
	return success(out, "Execution host ready: "+setupHostLabel(&prepared)+".", map[string]any{"host": prepared.Host})
}

// Setup and installation use the same CLI preparation, even after a frontend
// upgrade. Read-only status and client launches never bootstrap a host.
func ensureSetupExecutionHost(ctx context.Context, frontend management.Store, selected management.ExecutionSelection, remember bool, diagnostics io.Writer) (management.ExecutionSelection, error) {
	if selected.Host.Kind == "local" && !selected.System {
		return selected, nil
	}
	deps := defaultRemotePreparationDependencies()
	if selected.Owned {
		reusable := true
		if selected.Host.Kind == "wsl" {
			var err error
			reusable, err = verifiedOwnedWSLHost(ctx, frontend, selected, deps.run)
			if err != nil {
				return selected, err
			}
		}
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var version githubRelayOutput
		err := deps.run(probeCtx, selected, []string{"version"}, nil, &version, io.Discard)
		cancel()
		if reusable && err == nil && !version.overflow && strings.TrimSpace(version.String()) == "loki "+management.ManagerRelease {
			return selected, nil
		}
		if selected.System {
			var prepared *management.ExecutionSelection
			var err error
			if remember {
				prepared, err = prepareLocalSystemHost(ctx, frontend, os.Stdin, diagnostics)
			} else {
				prepared, err = management.PrepareSystemHostTarget(ctx, frontend, os.Stdin, diagnostics)
			}
			if err != nil {
				return selected, err
			}
			if prepared == nil {
				return selected, fmt.Errorf("system host preparation did not return a login-user host")
			}
			return *prepared, nil
		}
		// Owned distributions use their ownership-checked publisher, not the
		// private manager path used for existing user distributions.
		if selected.Host.Kind != "wsl" {
			return selected, fmt.Errorf("prepare the owned execution host with loki setup before using it")
		}
		prepared, err := prepareManagedHostFor(ctx, frontend, selected.Host.Distribution, remember, diagnostics)
		if err != nil {
			return selected, err
		}
		return *prepared, nil
	}
	prepared, err := prepareRemoteManager(ctx, selected, diagnostics, deps)
	if err != nil {
		return selected, err
	}
	if remember && prepared != selected {
		if err := frontend.SelectExecutionHost(prepared); err != nil {
			return selected, err
		}
	}
	return prepared, nil
}

func verifiedOwnedWSLHost(ctx context.Context, frontend management.Store, selected management.ExecutionSelection, run func(context.Context, management.ExecutionSelection, []string, io.Reader, io.Writer, io.Writer) error) (bool, error) {
	record, err := frontend.HostPreparationFor(selected.Host.Distribution)
	if err != nil {
		return false, err
	}
	if record == nil {
		return false, nil
	}
	probe := selected
	probe.Command = "/usr/bin/cat"
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out githubRelayOutput
	if err := run(ctx, probe, []string{"/var/lib/loki-host-owner"}, nil, &out, io.Discard); err != nil || out.overflow || strings.TrimSpace(out.String()) != record.Token {
		return false, fmt.Errorf("managed WSL ownership changed; existing distribution and data were retained")
	}
	return record.Phase == "ready", nil
}
