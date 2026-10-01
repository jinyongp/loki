package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"golang.org/x/mod/semver"
	windowshost "loki/internal/host/windows"
)

type productUpdateOptions struct {
	All                 bool
	Distribution        string
	InterruptActiveJobs bool
}

func parseProductUpdateOptions(args []string, defaultDistribution string) (productUpdateOptions, error) {
	var options productUpdateOptions
	flags := flag.NewFlagSet("loki update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.All, "all", false, "update the frontend and appliance")
	flags.StringVar(&options.Distribution, "distribution", defaultDistribution, "WSL distribution name")
	flags.BoolVar(&options.InterruptActiveJobs, "interrupt-active-jobs", false, "approve interrupting active jobs")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if !options.All || flags.NArg() != 0 {
		return options, errors.New("use 'loki update --all' to update both the frontend and appliance")
	}
	if err := windowshost.ValidateDistributionName(options.Distribution); err != nil {
		return options, err
	}
	return options, nil
}

type allUpdateDependencies struct {
	Executable string
	ReleaseTag string
	Run        func(context.Context, string, []string, io.Writer, io.Writer) int
}

func runAllApplianceUpdateWith(ctx context.Context, deps allUpdateDependencies, options productUpdateOptions, stdout, stderr io.Writer) (code int) {
	if deps.Run == nil || deps.Executable == "" || !semver.IsValid(deps.ReleaseTag) {
		fmt.Fprintln(stderr, "combined Loki update is not configured")
		return 1
	}
	defer func() {
		if code != 0 {
			interrupt := ""
			if options.InterruptActiveJobs {
				interrupt = " --interrupt-active-jobs"
			}
			fmt.Fprintf(stderr, "The Windows frontend remains updated. Rerun 'loki update --all --distribution %s%s' to continue the appliance update.\n", options.Distribution, interrupt)
		}
	}()
	// Invoke the verified installed frontend so newer appliance compatibility
	// handling takes effect immediately after a frontend replacement.
	run := func(action string, extra []string, output io.Writer) int {
		if err := ctx.Err(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		args := []string{"update", action, "--distribution", options.Distribution}
		return deps.Run(ctx, deps.Executable, append(args, extra...), output, stderr)
	}
	status := func() (machineUpdateStatus, int) {
		var raw bytes.Buffer
		if exit := run("status", []string{"--json"}, &raw); exit != 0 {
			return machineUpdateStatus{}, exit
		}
		value, err := decodeMachineJSON[machineUpdateStatus](raw.String())
		if err != nil {
			fmt.Fprintln(stderr, "cannot inspect Loki appliance update status:", err)
			return machineUpdateStatus{}, 1
		}
		if value.Installed == nil || !semver.IsValid(generationVersion(value.Installed)) {
			fmt.Fprintln(stderr, "Loki appliance update status has no valid installed release")
			return machineUpdateStatus{}, 1
		}
		return value, 0
	}
	current, code := status()
	if code != 0 {
		return code
	}
	installed := generationVersion(current.Installed)
	if comparison := semver.Compare(installed, deps.ReleaseTag); comparison >= 0 {
		if comparison > 0 {
			fmt.Fprintf(stdout, "Loki appliance %s is newer than the published release %s; leaving it in place.\n", installed, deps.ReleaseTag)
		} else {
			// An earlier apply may have committed before its health or Windows
			// connection refresh failed. Check readiness and finish the refresh.
			for _, args := range [][]string{
				{"doctor", "--distribution", options.Distribution},
				{"connection", "show", "--distribution", options.Distribution, "local"},
			} {
				if err := ctx.Err(); err != nil {
					fmt.Fprintln(stderr, err)
					return 1
				}
				var report bytes.Buffer
				if code = deps.Run(ctx, deps.Executable, args, &report, stderr); code != 0 {
					_, _ = io.Copy(stdout, &report)
					return code
				}
			}
			fmt.Fprintf(stdout, "Loki frontend and appliance are current at %s.\n", deps.ReleaseTag)
		}
		return 0
	}
	if current.Prepared == nil || generationVersion(current.Available) != deps.ReleaseTag {
		if code = run("prepare", nil, stdout); code != 0 {
			return code
		}
		current, code = status()
		if code != 0 {
			return code
		}
	}
	if err := updateApplyReadinessError(current); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if generationVersion(current.Available) != deps.ReleaseTag {
		fmt.Fprintln(stderr, "prepared appliance update does not match the verified frontend release; rerun 'loki update --all'")
		return 1
	}
	extra := []string{"--approve"}
	if options.InterruptActiveJobs {
		extra = append(extra, "--interrupt-active-jobs")
	}
	if code = run("apply", extra, stdout); code != 0 {
		return code
	}
	current, code = status()
	if code != 0 {
		return code
	}
	if generationVersion(current.Installed) != deps.ReleaseTag {
		fmt.Fprintln(stderr, "Loki appliance release did not match the applied update")
		return 1
	}
	fmt.Fprintf(stdout, "Loki frontend and appliance are current at %s.\n", deps.ReleaseTag)
	return 0
}

func productUpdateHelpRequested(args []string) bool {
	if len(args) == 0 || !strings.HasPrefix(args[0], "-") {
		return false
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}
