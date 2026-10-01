package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"loki/internal/host/githubsetup"
	windowshost "loki/internal/host/windows"
)

func runWindowsGitHubBrowserSetup(ctx context.Context, distribution string, interrupt bool, browser githubsetup.Options, stdout, stderr io.Writer) int {
	client := windowshost.NewWindowsOperatorClient()
	transport := func(ctx context.Context, input githubsetup.Request) (githubsetup.View, error) {
		raw, err := json.Marshal(input)
		if err != nil {
			return githubsetup.View{}, errors.New("cannot encode GitHub browser relay")
		}
		defer clear(raw)
		request := windowshost.OperatorRequest{Command: "integration", Action: "setup", Integration: "github", UseStdin: true, GitHubBrowser: true, InterruptActiveJobs: interrupt}
		result, err := executeWindowsIntegrationWithProgress(ctx, client, distribution, request, raw, stderr)
		if err != nil {
			return githubsetup.View{}, err
		}
		if result.Probe.ExitCode != 0 {
			message := strings.TrimSpace(result.Probe.Stderr)
			if message == "" {
				message = "GitHub setup failed in the Loki appliance"
			}
			return githubsetup.View{}, errors.New(message)
		}
		var view githubsetup.View
		if err = json.Unmarshal([]byte(result.Probe.Stdout), &view); err != nil || view.SchemaVersion != 1 {
			return view, errors.New("invalid GitHub setup response from the Loki appliance")
		}
		return view, nil
	}
	if err := githubsetup.Run(ctx, transport, browser, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
