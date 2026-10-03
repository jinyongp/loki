package main

import (
	"context"
	"encoding/json"
	"errors"

	"loki/internal/integrations/github/setup"
	windowshost "loki/internal/host/windows"
)

func windowsGitHubUserTransport(client windowshost.OperatorClient, distribution string) githubsetup.UserTransport {
	return func(ctx context.Context, request githubsetup.UserRequest) (githubsetup.UserView, error) {
		raw, err := json.Marshal(request)
		if err != nil {
			return githubsetup.UserView{}, errors.New("cannot encode GitHub user authorization request")
		}
		result, err := client.ExecuteInput(ctx, distribution, windowshost.OperatorRequest{
			Command: "integration", Action: "login", Integration: "github", GitHubUser: true, UseStdin: true,
		}, raw)
		if err != nil {
			return githubsetup.UserView{}, githubsetup.UserLoginError("")
		}
		if result.Probe.ExitCode != 0 {
			return githubsetup.UserView{}, githubsetup.UserLoginError(result.Probe.Stderr)
		}
		var view githubsetup.UserView
		if json.Unmarshal([]byte(result.Probe.Stdout), &view) != nil || view.Status == "" {
			return view, errors.New("invalid GitHub user authorization response from the Loki appliance")
		}
		if err = view.Validate(request); err != nil {
			return view, err
		}
		return view, nil
	}
}
