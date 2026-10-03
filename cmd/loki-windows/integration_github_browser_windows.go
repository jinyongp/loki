package main

import (
	"context"
	"fmt"
	"io"

	"loki/internal/integrations/github/setup"
	windowshost "loki/internal/host/windows"
)

func runWindowsGitHubBrowserSetup(ctx context.Context, distribution string, interrupt bool, browser githubsetup.Options, stdout, stderr io.Writer) int {
	client := windowshost.NewWindowsOperatorClient()
	transport := windowsGitHubSetupTransport(client, distribution, interrupt, stderr)
	browser.UserTransport = windowsGitHubUserTransport(client, distribution)
	if err := githubsetup.Run(ctx, transport, browser, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
