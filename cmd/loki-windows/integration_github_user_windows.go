package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"loki/internal/host/githubsetup"
	windowshost "loki/internal/host/windows"
)

func runWindowsGitHubUser(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
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
	account := flags.String("account", "", "configured personal GitHub account")
	noBrowser := flags.Bool("no-browser", false, "print the GitHub device login URL")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil || flags.NArg() != 0 || strings.TrimSpace(*account) == "" || *noBrowser && action != "login" {
		printIntegrationUsage(stderr, action)
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	transport := windowsGitHubUserTransport(windowshost.NewWindowsOperatorClient(), *distribution)
	owner := strings.ToLower(strings.TrimSpace(*account))
	var err error
	if action == "login" {
		err = githubsetup.RunUser(ctx, transport, owner, githubsetup.Options{NoBrowser: *noBrowser}, stdout)
	} else {
		operation := "status"
		if action == "logout" {
			operation = "logout"
		}
		view, callErr := transport(ctx, githubsetup.UserRequest{Action: operation, Account: owner})
		err = callErr
		if err == nil {
			err = json.NewEncoder(stdout).Encode(view)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
