package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/host/appliance"
	"loki/internal/host/diagnostics"
)

func runHostAppliance(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "check" && args[0] != "repair") {
		fmt.Fprintln(stderr, "usage: loki host appliance check [--root PATH] | repair --approve")
		return 2
	}
	flags := flag.NewFlagSet("host appliance "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "inspect required files in an extracted WSL image")
	approve := flags.Bool("approve", false, "approve managed WSL prerequisite repair")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 ||
		(args[0] == "check" && *approve) || (args[0] == "repair" && (!*approve || *root != "")) {
		fmt.Fprintln(stderr, "repair requires --approve; --root is valid only for check")
		return 2
	}
	host := appliance.Host{Root: *root}
	if *root != "" {
		if !filepath.IsAbs(*root) || filepath.Clean(*root) != *root {
			fmt.Fprintln(stderr, "--root must be a clean absolute path")
			return 2
		}
		missing := host.MissingFiles()
		if len(missing) != 0 {
			fmt.Fprintf(stderr, "WSL image lacks required files: %v\n", missing)
			return 1
		}
		fmt.Fprintln(stdout, "WSL image prerequisites are present")
		return 0
	}
	managed, err := host.Managed()
	if err != nil || !managed {
		fmt.Fprintln(stderr, "this command requires a managed Loki WSL appliance")
		return 1
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "WSL appliance diagnostics and repair require root")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if args[0] == "repair" {
		lock, lockErr := os.OpenFile("/var/lib/loki-appliance/prerequisites.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
		if lockErr != nil {
			fmt.Fprintln(stderr, lockErr)
			return 1
		}
		defer lock.Close()
		if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			fmt.Fprintln(stderr, "another WSL prerequisite repair is running")
			return 1
		}
		if err = host.Repair(ctx); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	report, err := diagnostics.NewReport(time.Now().UTC(), host.Inspect(ctx)...)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return encodeHostApplianceReport(stdout, stderr, report)
}

func encodeHostApplianceReport(stdout, stderr io.Writer, report diagnostics.Report) int {
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !report.Healthy() {
		return 1
	}
	return 0
}

func repairManagedAppliance(ctx context.Context) error {
	managed, err := (appliance.Host{}).Managed()
	if err != nil || !managed {
		return err
	}
	// Use the command boundary so every entry point gets the same approval,
	// timeout, root requirement, serialization, and post-repair validation.
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	_, err = appliance.Exec(ctx, binary, "host", "appliance", "repair", "--approve")
	return err
}
