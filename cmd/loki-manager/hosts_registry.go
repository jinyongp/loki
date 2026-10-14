package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"loki/internal/management"
	"loki/internal/tools"
)

func runHosts(ctx context.Context, store management.Store, args []string, out, diagnostics io.Writer) error {
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			return fmt.Errorf("usage: loki hosts list")
		}
		r, err := store.HostRegistry()
		if err != nil {
			return err
		}
		if jsonOutput(out) {
			return result(out, "Loki hosts", r)
		}
		fmt.Fprintln(out, "Registered execution hosts")
		for _, h := range r.Hosts {
			label := h.Name
			if h.ID == r.Default {
				label += " (default)"
			}
			fmt.Fprintf(out, "\n  %s\n    Target: %s\n", label, setupHostLabel(&h.Target))
			if h.Target.Root != "" {
				fmt.Fprintln(out, "    State:", h.Target.Root)
			}
		}
		fmt.Fprintln(out, "\nUse 'loki hosts use NAME' to change the CLI default. Existing connections keep their targets.")
		return nil
	}
	if args[0] == "add" {
		f := flag.NewFlagSet("hosts add", flag.ContinueOnError)
		f.SetOutput(diagnostics)
		kind := f.String("type", "", "local, wsl or ssh")
		distribution := f.String("distribution", "", "WSL distribution")
		address := f.String("address", "", "SSH destination")
		root := f.String("root", "", "execution-host state directory")
		command := f.String("command", "loki", "manager executable")
		user := f.String("user", "", "WSL user")
		managed := f.Bool("managed", false, "Loki-owned WSL host; preparation is explicit")
		if err := f.Parse(toolArguments(args[1:])); err != nil {
			return err
		}
		if f.NArg() != 1 {
			return fmt.Errorf("usage: loki hosts add NAME --type local|wsl|ssh [OPTIONS]")
		}
		if *managed {
			if *kind != "wsl" {
				return fmt.Errorf("--managed requires --type wsl")
			}
			if *distribution == "" {
				*distribution = "loki-tools-" + f.Arg(0)
			}
			*user = "root"
			*root = "/var/lib/loki-tools"
			*command = "/usr/local/bin/loki"
		}
		target := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: *kind, Distribution: *distribution, Address: *address}, Root: *root, Command: *command, User: *user, Owned: *managed}
		h, err := store.RegisterHost(f.Arg(0), target)
		if err != nil {
			return err
		}
		return success(out, "Host registered: "+h.Name+". Run 'loki hosts prepare "+h.Name+"' to prepare it, or 'loki hosts use "+h.Name+"' to select it. No tools were installed.", map[string]any{"host": h})
	}
	if args[0] == "rename" {
		if len(args) != 3 {
			return fmt.Errorf("usage: loki hosts rename NAME NEW-NAME")
		}
		if err := store.RenameHost(args[1], args[2]); err != nil {
			return err
		}
		return success(out, "Host renamed; existing connections retain their target.", nil)
	}
	if args[0] == "show" {
		r, err := store.HostRegistry()
		if err != nil {
			return err
		}
		h := r.Selected()
		if len(args) == 2 {
			h, err = r.Find(args[1])
			if err != nil {
				return err
			}
		} else if len(args) != 1 {
			return fmt.Errorf("usage: loki hosts show [NAME]")
		}
		if jsonOutput(out) {
			return result(out, "Loki host", h)
		}
		fmt.Fprintf(out, "Host: %s\n  ID: %s\n  Target: %s\n  Default: %t\n", h.Name, h.ID, setupHostLabel(&h.Target), h.ID == r.Default)
		if h.Target.Root != "" {
			fmt.Fprintln(out, "  State directory:", h.Target.Root)
		}
		return nil
	}
	if args[0] == "use" && len(args) == 2 && args[1] != "wsl" && args[1] != "ssh" && args[1] != "local" {
		if err := store.UseHost(args[1]); err != nil {
			return err
		}
		return success(out, "Default execution host: "+args[1]+". Existing client connections are unchanged.", nil)
	}
	if args[0] == "prepare" && len(args) == 2 && args[1] != "wsl" && args[1] != "ssh" && args[1] != "local" {
		h, err := store.RegisteredHost(args[1])
		if err != nil {
			return err
		}
		target := h.Target
		if !target.Remote() {
			return success(out, "Native host is available; use loki setup to install tools.", nil)
		}
		fmt.Fprintln(diagnostics, "Preparing registered host:", h.Name)
		prepared, err := ensureSetupExecutionHost(ctx, store, target, false, diagnostics)
		if err != nil {
			return err
		}
		if prepared != target {
			if err := store.UpdateHostTarget(h.ID, target, prepared); err != nil {
				return err
			}
		}
		return success(out, "Host prepared: "+h.Name+". The CLI default and client bindings are retained.", nil)
	}
	if args[0] == "remove" && len(args) >= 2 && !strings.HasPrefix(args[1], "--") {
		purge := len(args) == 3 && args[2] == "--purge"
		if len(args) > 2 && !purge {
			return fmt.Errorf("usage: loki hosts remove NAME [--purge]")
		}
		h, err := store.RegisteredHost(args[1])
		if err != nil {
			return err
		}
		if err := requireRegisteredHostDetached(store, h); err != nil {
			return err
		}
		if purge {
			// Multiple installations can share the same physical distribution/service.
			r, err := store.HostRegistry()
			if err != nil {
				return err
			}
			for _, other := range r.Hosts {
				if other.ID != h.ID && sameHostEnvironment(other.Target.Host, h.Target.Host) && (h.Target.Host.Kind == "wsl" || h.Target.System && other.Target.System) {
					return fmt.Errorf("host infrastructure is also referenced by %q; preserving it", other.Name)
				}
			}
			fmt.Fprintln(diagnostics, "Removing owned host:", h.Name)
			if err := removeManagedHost(ctx, store, h.Target, diagnostics); err != nil {
				return err
			}
			if h.Target.Host.Kind == "wsl" {
				if err := store.ForgetHostPreparation(h.Target.Host.Distribution); err != nil {
					return err
				}
			}
		}
		if err := store.RemoveHost(h.ID); err != nil {
			return err
		}
		return success(out, "Host registration removed: "+h.Name+". Other hosts are retained.", map[string]any{"host_removed": purge, "data_retained": !purge})
	}
	if args[0] == "remove" {
		r, err := store.HostRegistry()
		if err != nil {
			return err
		}
		if len(args) == 1 {
			return fmt.Errorf("name the host: loki hosts remove %s (add --purge only to delete owned infrastructure)", r.Selected().Name)
		}
		if len(args) == 2 && args[1] == "--purge" {
			return fmt.Errorf("name the host before deletion: loki hosts remove %s --purge", r.Selected().Name)
		}
	}
	return runLegacyHosts(ctx, store, args, out, diagnostics)
}

func requireRegisteredHostDetached(store management.Store, h management.HostRecord) error {
	config, err := defaultCodexConfig()
	if err != nil {
		return err
	}
	raw, err := readCodexConfiguration(config)
	if err != nil {
		return err
	}
	if strings.Contains(string(raw), "host-id="+h.ID) || legacyCodexMatches(raw, &h.Target, store.Root) {
		return fmt.Errorf("disconnect this host's Codex connection before removing %q", h.Name)
	}
	return requireManagedHostDetached(store, h)
}

// Preparation callers can use a registered host without relying on the default.
func registeredConnectionArguments(store management.Store, target *management.ExecutionSelection, args []string) []string {
	if target != nil {
		if h, err := store.HostForTarget(*target); err == nil {
			return append(append([]string{}, args...), "--host-id", h.ID)
		}
	}
	return args
}

func sameHostEnvironment(a, b tools.Host) bool {
	if a.Kind == "wsl" && b.Kind == "wsl" {
		return strings.EqualFold(a.Distribution, b.Distribution)
	}
	return a == b
}
