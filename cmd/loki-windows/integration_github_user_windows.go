package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"loki/internal/host/githubsetup"
	windowshost "loki/internal/host/windows"
)

func runWindowsGitHubUser(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	if action != "logout" {
		fmt.Fprintln(stderr, "Use loki integration status github to inspect GitHub authorization.")
		return 2
	}
	if len(args) > 0 && args[0] == "github" {
		args = args[1:]
	} else if len(args) > 0 && args[len(args)-1] == "github" {
		args = args[:len(args)-1]
	} else {
		printIntegrationUsage(stderr, action)
		return 2
	}
	flags := flag.NewFlagSet("integration "+action+" github", flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil || flags.NArg() != 0 {
		printIntegrationUsage(stderr, action)
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	transport := windowsGitHubUserTransport(windowshost.NewWindowsOperatorClient(), *distribution)
	view, err := transport(ctx, githubsetup.UserRequest{Action: "logout"})
	if err == nil {
		err = json.NewEncoder(stdout).Encode(view)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
