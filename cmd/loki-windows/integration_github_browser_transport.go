package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"loki/internal/integrations/github/setup"
	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

func windowsGitHubSetupTransport(client windowshost.OperatorClient, distribution string, interrupt bool, stderr io.Writer) githubsetup.Transport {
	return func(ctx context.Context, input githubsetup.Request) (githubsetup.View, error) {
		raw, err := json.Marshal(input)
		if err != nil {
			return githubsetup.View{}, errors.New("cannot encode GitHub browser relay")
		}
		defer clear(raw)
		request := windowshost.OperatorRequest{Command: "integration", Action: "setup", Integration: "github", UseStdin: true, GitHubBrowser: true, InterruptActiveJobs: interrupt}
		var result windowshost.OperatorResult
		if input.Action == "poll" || input.Action == "exchange" || input.Action == "finish" || input.Action == "select" {
			// The shared browser flow already announces these phases. Background
			// requests must not repeat WSL and configuration inspection progress.
			result, err = client.ExecuteInput(ctx, distribution, request, raw)
		} else {
			result, err = executeWindowsIntegrationWithProgress(ctx, client, distribution, request, raw, stderr)
		}
		if err != nil {
			return githubsetup.View{}, err
		}
		if result.Probe.ExitCode != 0 {
			message := progress.NonProgressText(result.Probe.Stderr)
			if message == "" {
				message = "GitHub setup failed in the Loki appliance"
			}
			return githubsetup.View{}, errors.New(message)
		}
		var view githubsetup.View
		if err = json.Unmarshal([]byte(result.Probe.Stdout), &view); err != nil || view.SchemaVersion != 1 {
			return view, errors.New("invalid GitHub setup response from the Loki appliance")
		}
		if detail := progress.NonProgressText(result.Probe.Stderr); detail != "" {
			fmt.Fprintln(stderr, detail)
		}
		return view, nil
	}
}
