//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	windowshost "loki/internal/host/windows"
)

func runConnection(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if connectionHelpRequested(args) {
		printConnectionHelp(stdout, args[:len(args)-1]...)
		return 0
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return runConnectionList(ctx, args, stdout, stderr)
	}
	switch args[0] {
	case "list":
		return runConnectionList(ctx, args[1:], stdout, stderr)
	case "show":
		return runConnectionShow(ctx, args[1:], stdout, stderr)
	case "setup", "start", "stop", "remove":
		return runConnectionMutation(ctx, args[0], args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown connection command %q\n", args[0])
		printConnectionHelp(stderr)
		return 2
	}
}

func runConnectionList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki connection list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		printConnectionHelp(stdout, "list")
		return 0
	} else if err != nil || flags.NArg() != 0 {
		if err != nil {
			fmt.Fprintln(stderr, err)
		}
		printConnectionHelp(stderr, "list")
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	localPresent, err := passiveLocalConnectionPresent(*distribution)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	manager, err := newWindowsConnectionManager()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	descriptors, err := manager.ProviderDescriptors()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	states, err := manager.ConfiguredStates(*distribution)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	items, err := buildConnectionList(localPresent, descriptors, states)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOutput {
		if err = writeConnectionListJSON(*distribution, items, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	renderConnectionList(*distribution, items, stdout)
	return 0
}

func runConnectionShow(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki connection show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		printConnectionHelp(stdout, "show")
		return 0
	} else if err != nil || flags.NArg() != 1 {
		if err != nil {
			fmt.Fprintln(stderr, err)
		} else {
			fmt.Fprintln(stderr, "connection show requires one NAME")
		}
		printConnectionHelp(stderr, "show")
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	name := strings.TrimSpace(flags.Arg(0))
	if name == localConnectionID {
		local, err := loadLocalConnectionView(ctx, *distribution)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOutput {
			if err = writeLocalConnectionJSON(*distribution, local, stdout); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
		renderLocalConnection(*distribution, local, stdout)
		return 0
	}

	manager, err := newWindowsConnectionManager()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	descriptors, err := manager.ProviderDescriptors()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	descriptor, ok := findConnectionDescriptor(descriptors, name)
	if !ok {
		fmt.Fprintf(stderr, "unknown connection %q; run 'loki connection list' to see available connections\n", name)
		return 2
	}
	status, err := manager.Status(ctx, *distribution, descriptor.ID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOutput {
		if err = writeManagedConnectionJSON(*distribution, descriptor, status, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	renderManagedConnection(*distribution, descriptor, status, stdout)
	return 0
}

func findConnectionDescriptor(
	descriptors []windowshost.ConnectionProviderDescriptor,
	id string,
) (windowshost.ConnectionProviderDescriptor, bool) {
	for _, descriptor := range descriptors {
		if descriptor.ID == id {
			return descriptor, true
		}
	}
	return windowshost.ConnectionProviderDescriptor{}, false
}

func passiveLocalConnectionPresent(distribution string) (bool, error) {
	if err := windowshost.ValidateDistributionName(distribution); err != nil {
		return false, err
	}
	expected, err := expectedInstallation(distribution, windowshost.InstallOptions{Distribution: distribution})
	if err != nil {
		return false, err
	}
	snapshot, err := windowshost.NewWindowsReplicaStore().Read(expected)
	if err != nil {
		return false, fmt.Errorf("read local Loki MCP connection: %w", err)
	}
	return snapshot.Present, nil
}

func loadLocalConnectionView(ctx context.Context, distribution string) (localConnectionView, error) {
	if err := windowshost.ValidateDistributionName(distribution); err != nil {
		return localConnectionView{}, err
	}
	expected, err := expectedInstallation(distribution, windowshost.InstallOptions{Distribution: distribution})
	if err != nil {
		return localConnectionView{}, err
	}
	result, err := windowshost.NewWindowsReplicaSynchronizer(nil).Sync(ctx, expected)
	if err != nil {
		return localConnectionView{}, fmt.Errorf("refresh local Loki MCP connection: %w", err)
	}
	snapshot, err := windowshost.NewWindowsReplicaStore().Read(expected)
	if err != nil {
		return localConnectionView{}, fmt.Errorf("read local Loki MCP connection: %w", err)
	}
	if !snapshot.Present || strings.TrimSpace(snapshot.LocalOrigin) == "" {
		return localConnectionView{}, errors.New("local Loki MCP connection is unavailable")
	}
	return localConnectionView{
		LocalOrigin:     snapshot.LocalOrigin,
		TokenFile:       filepath.Join(expected.StateDir, "mcp-token"),
		HostState:       result.Status.State,
		Release:         result.Status.Release,
		UpdatePrepared:  result.Status.UpdatePrepared,
		UpdateAvailable: result.Status.UpdateAvailable,
	}, nil
}
