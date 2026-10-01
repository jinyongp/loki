package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	windowshost "loki/internal/host/windows"
)

type integrationActionOptions struct {
	Distribution  string
	JSON          bool
	InterruptJobs bool
	Name          string
}

func parseIntegrationAction(action string, args []string, distribution string) (integrationActionOptions, error) {
	flags := flag.NewFlagSet("integration "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := integrationActionOptions{Distribution: distribution}
	flags.StringVar(&options.Distribution, "distribution", distribution, "WSL distribution name")
	flags.BoolVar(&options.JSON, "json", false, "emit machine-readable JSON")
	flags.BoolVar(&options.InterruptJobs, "interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if err := windowshost.ValidateDistributionName(options.Distribution); err != nil {
		return options, err
	}
	if action == "list" {
		if flags.NArg() != 0 {
			return options, errors.New("integration list does not accept a NAME argument")
		}
	} else {
		if flags.NArg() != 1 {
			return options, fmt.Errorf("integration %s requires one NAME: browser, signing, or github", action)
		}
		options.Name = strings.ToLower(strings.TrimSpace(flags.Arg(0)))
		if options.Name != "browser" && options.Name != "signing" && options.Name != "github" {
			return options, errors.New("integration name must be browser, signing, or github")
		}
	}
	if (action == "list" || action == "status" || action == "doctor") && options.InterruptJobs {
		return options, errors.New("--interrupt-active-jobs is valid only for integration mutations")
	}
	if action != "list" && action != "status" && action != "doctor" && options.JSON {
		return options, errors.New("--json is valid only for list, status, or doctor")
	}
	return options, nil
}
