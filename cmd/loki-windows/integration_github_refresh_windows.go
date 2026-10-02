package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	windowshost "loki/internal/host/windows"
)

func runWindowsGitHubRefresh(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "github" {
		args = args[1:]
	} else if len(args) > 0 && args[len(args)-1] == "github" {
		args = args[:len(args)-1]
	} else {
		printIntegrationUsage(stderr, "refresh")
		return 2
	}
	flags := flag.NewFlagSet("integration refresh github", flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		printIntegrationUsage(stdout, "refresh")
		return 0
	} else if err != nil || flags.NArg() != 0 {
		printIntegrationUsage(stderr, "refresh")
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	result, err := windowshost.NewWindowsOperatorClient().Execute(ctx, *distribution, windowshost.OperatorRequest{Command: "integration", Action: "refresh", Integration: "github"})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeNativeProbe(result.Probe, stdout, stderr)
}
