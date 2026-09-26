package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"loki/internal/host/lifecycle"
)

type hostIngressOptions struct {
	System    bool
	StateRoot string
	JSON      bool
	Host      string
}

type hostIngressRuntime interface {
	SetIngressHosts(context.Context, []string) error
}

var openHostIngressRuntime = func(store *lifecycle.FileStore) (hostIngressRuntime, error) {
	return newHostComposeBackend(store)
}

type hostIngressReport struct {
	PublicHosts []string `json:"public_hosts"`
}

func parseHostIngressOptions(action string, args []string, stderr io.Writer) (hostIngressOptions, error) {
	if action != "list" && action != "allow" && action != "remove" {
		return hostIngressOptions{}, errors.New("usage: loki host ingress list|allow|remove [--system] [--state-root PATH] [--json] [HOST]")
	}
	flags := flag.NewFlagSet("host ingress "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "operate on the system-wide host installation")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return hostIngressOptions{}, err
	}
	wantArgs := 0
	if action != "list" {
		wantArgs = 1
	}
	if flags.NArg() != wantArgs {
		return hostIngressOptions{}, errors.New("usage: loki host ingress list|allow|remove [--system] [--state-root PATH] [--json] [HOST]")
	}
	result := hostIngressOptions{
		System: *system, StateRoot: strings.TrimSpace(*stateRoot), JSON: *jsonOutput,
	}
	if wantArgs == 1 {
		result.Host = strings.ToLower(strings.TrimSpace(flags.Arg(0)))
	}
	if result.StateRoot != "" && (!filepath.IsAbs(result.StateRoot) || filepath.Clean(result.StateRoot) != result.StateRoot ||
		result.StateRoot == string(filepath.Separator) || strings.ContainsRune(result.StateRoot, 0)) {
		return hostIngressOptions{}, errors.New("--state-root must be a clean absolute non-root path")
	}
	return result, nil
}

func runHostIngress(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: loki host ingress list|allow|remove [--system] [--state-root PATH] [--json] [HOST]")
		return 2
	}
	action := args[0]
	options, err := parseHostIngressOptions(action, args[1:], stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if options.System && os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "system host ingress commands require root")
		return 1
	}
	if options.StateRoot == "" {
		options.StateRoot, err = defaultHostStateRoot(options.System)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	store, err := lifecycle.OpenFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || snapshot.Installed == nil || snapshot.Installation == nil {
		fmt.Fprintln(stderr, "Loki does not have an active installed release.")
		return 1
	}
	current := append([]string(nil), snapshot.Host.IngressHosts...)
	if action == "list" {
		return writeHostIngressState(stdout, stderr, options.JSON, current)
	}

	host, err := lifecycle.NormalizeIngressHosts([]string{options.Host})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	options.Host = host[0]

	next := append([]string(nil), current...)
	switch action {
	case "allow":
		next = append(next, options.Host)
	case "remove":
		filtered := next[:0]
		for _, host := range next {
			if host != options.Host {
				filtered = append(filtered, host)
			}
		}
		next = filtered
	}
	next, err = lifecycle.NormalizeIngressHosts(next)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	runtime, err := openHostIngressRuntime(store)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = applyHostIngressState(context.Background(), store, runtime, current, next); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeHostIngressState(stdout, stderr, options.JSON, next)
}

func applyHostIngressState(
	ctx context.Context,
	store *lifecycle.FileStore,
	runtime hostIngressRuntime,
	current, next []string,
) error {
	if store == nil || runtime == nil {
		return errors.New("host ingress runtime is not configured")
	}
	changed := !slices.Equal(current, next)
	if changed {
		if err := store.CommitIngressHosts(ctx, next, lifecycleTimeNow()); err != nil {
			return err
		}
	}
	if err := runtime.SetIngressHosts(ctx, next); err == nil {
		return nil
	} else {
		applyErr := err
		if !changed {
			return applyErr
		}
		rollbackErr := store.CommitIngressHosts(context.Background(), current, lifecycleTimeNow())
		if rollbackErr != nil {
			return errors.Join(applyErr, fmt.Errorf("restore previous MCP ingress host state: %w", rollbackErr))
		}
		return applyErr
	}
}

func writeHostIngressState(stdout, stderr io.Writer, jsonOutput bool, hosts []string) int {
	report := hostIngressReport{PublicHosts: append([]string(nil), hosts...)}
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			fmt.Fprintln(stderr, "cannot encode MCP ingress state")
			return 1
		}
		return 0
	}
	fmt.Fprintln(stdout, "Loki MCP ingress allowlist")
	if len(report.PublicHosts) == 0 {
		fmt.Fprintln(stdout, "  Public hosts: none")
	} else {
		for _, host := range report.PublicHosts {
			fmt.Fprintf(stdout, "  - %s\n", host)
		}
	}
	fmt.Fprintln(stdout, "External ingress infrastructure remains operator-managed.")
	return 0
}
