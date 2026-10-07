package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"loki/internal/management"
	"loki/internal/tools"
)

func runHosts(ctx context.Context, store management.Store, args []string, out, diagnostics io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && args[0] == "show") {
		selected, err := store.ExecutionSelection()
		if err != nil {
			return err
		}
		if selected == nil {
			selected = &management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "local"}, Command: "loki"}
		}
		return result(out, "Loki execution host", selected)
	}
	if args[0] == "remove" {
		purge := len(args) == 2 && args[1] == "--purge"
		if len(args) != 1 && !purge {
			return fmt.Errorf("usage: loki hosts remove [--purge]")
		}
		selected, err := store.ExecutionSelection()
		if err != nil {
			return err
		}
		if !purge {
			if err := requireHostConnectionsDetached(store); err != nil {
				return err
			}
		}
		if purge && selected != nil {
			if err := removeManagedHost(ctx, store, *selected, diagnostics); err != nil {
				return err
			}
		}
		if err := store.ForgetExecutionHost(purge); err != nil {
			return err
		}
		return success(out, "Execution host selection removed.", map[string]any{"host_removed": purge, "data_retained": !purge})
	}
	if args[0] != "use" && args[0] != "prepare" {
		return fmt.Errorf("choose hosts show or hosts use; see loki hosts --help")
	}
	f := flag.NewFlagSet("hosts use", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	distribution := f.String("distribution", "", "WSL distribution")
	address := f.String("address", "", "SSH destination")
	root := f.String("root", "", "management directory on the remote host")
	command := f.String("command", "loki", "manager executable on the execution host")
	user := f.String("user", "", "WSL execution user")
	if err := f.Parse(toolArguments(args[1:])); err != nil {
		return err
	}
	if f.NArg() != 1 {
		return fmt.Errorf("choose local, wsl or ssh; see loki hosts use --help")
	}
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: f.Arg(0), Distribution: *distribution, Address: *address}, Root: *root, Command: *command, User: *user}
	if err := selected.Validate(); err != nil {
		return err
	}
	if err := requireHostConnectionsDetached(store); err != nil {
		return err
	}
	if args[0] == "prepare" {
		if selected.Host.Kind == "local" {
			prepared, err := prepareLocalSystemHost(ctx, store, nil, diagnostics)
			if err != nil {
				return err
			}
			if prepared == nil {
				return success(out, "Local administrator host is ready.", nil)
			}
			return result(out, "Managed Linux host prepared", prepared)
		}
		if selected.Host.Kind == "wsl" {
			if runtime.GOOS != "windows" {
				return fmt.Errorf("managed WSL preparation is available on Windows; choose local Linux or SSH")
			}
			if selected.Host.Distribution != "loki-tools" {
				return fmt.Errorf("managed WSL preparation uses loki-tools; attach existing distributions with hosts use")
			}
			if _, err := prepareManagedHost(ctx, store, diagnostics); err != nil {
				return err
			}
			return success(out, "Managed WSL host prepared.", nil)
		}
		if selected.Host.Kind != "ssh" {
			return fmt.Errorf("hosts prepare supports wsl or ssh")
		}
		return prepareSSHHost(ctx, store, selected, out, diagnostics)
	}
	if selected.Host.Kind != "local" {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		fmt.Fprintln(diagnostics, "Checking the selected execution host...")
		relay, err := management.RelaySelection(ctx, selected, []string{"version"})
		if err != nil {
			return err
		}
		relay.Stderr = diagnostics
		var version githubRelayOutput
		relay.Stdout = &version
		if err := relay.Run(); err != nil {
			return fmt.Errorf("execution host is not ready; run loki hosts prepare ssh --address USER@HOST to install its CLI: %w", err)
		}
		if version.overflow {
			return fmt.Errorf("execution host version response exceeds its bound")
		}
		remoteVersion := strings.TrimPrefix(strings.TrimSpace(version.String()), "loki ")
		if !strings.HasPrefix(remoteVersion, "0.2.") {
			return fmt.Errorf("execution host requires the modular Loki CLI; run loki upgrade on that host")
		}
		if _, err := compareReleaseVersions(remoteVersion, management.ManagerRelease); err != nil {
			return fmt.Errorf("execution host does not provide a supported Loki manager")
		}
	}
	if err := store.SelectExecutionHost(selected); err != nil {
		return err
	}
	return success(out, "Execution host selected. Subsequent tools, status and integration commands use this host.", map[string]any{"host": selected.Host})
}
