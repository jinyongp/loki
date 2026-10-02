package main

import (
	"context"
	"time"

	"loki/internal/host/githubsetup"
)

type hostGitHubUserRuntime interface {
	GitHubUserAuthorization(context.Context, githubsetup.UserRequest) (githubsetup.UserView, error)
}

func inspectGitHubUserStatus(ctx context.Context, backend hostIntegrationRuntime, accounts []string, report *hostIntegrationReport) {
	available := false
	if runtime, ok := backend.(hostGitHubUserRuntime); ok {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		request := githubsetup.UserRequest{Action: "status"}
		view, err := runtime.GitHubUserAuthorization(ctx, request)
		if err == nil && view.Validate(request) == nil && len(view.Accounts) == len(accounts) {
			remaining := map[string]bool{}
			for _, account := range accounts {
				remaining[account] = true
			}
			for _, account := range view.Accounts {
				delete(remaining, account.Account)
			}
			if len(remaining) == 0 {
				report.PersonalProjects = &githubsetup.UserStatus{Status: view.Status, Accounts: view.Accounts}
				available = true
			}
		}
	}
	if !available {
		report.PersonalProjects = &githubsetup.UserStatus{Status: "unavailable"}
	}
}
