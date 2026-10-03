package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"loki/internal/config"
	githubsetup "loki/internal/integrations/github/setup"
	"loki/internal/management"
)

func githubUserTransport(backend management.IntegrationBackend) githubsetup.UserTransport {
	return func(ctx context.Context, request githubsetup.UserRequest) (githubsetup.UserView, error) {
		body := map[string]any{"operation": "github_user_" + request.Action}
		if request.Account != "" {
			body["account"] = request.Account
		}
		if request.SessionID != "" {
			body["session_id"] = request.SessionID
		}
		raw, err := backend.Administration(ctx, body)
		if err != nil {
			return githubsetup.UserView{}, githubsetup.UserLoginError(err.Error())
		}
		var view githubsetup.UserView
		if json.Unmarshal(raw, &view) != nil {
			return view, fmt.Errorf("invalid personal Projects authorization response")
		}
		return view, nil
	}
}

func runGitHubWizard(ctx context.Context, store management.Store, backend management.IntegrationBackend, personal, noBrowser bool, input io.Reader, output, diagnostics io.Writer) error {
	fmt.Fprintln(diagnostics, "Starting the protected GitHub setup service...")
	report, err := store.ReconcileFull(ctx, backend)
	if err != nil {
		// A fresh GitHub provider is intentionally unconfigured. All actual
		// services must be ready before permitting its administrator wizard.
		if report.Deployment == nil || report.Observation.Services["github"] == "" {
			return err
		}
		for _, service := range report.Deployment.Services {
			if report.Observation.Services[service] != "ready" {
				return err
			}
		}
	}
	var configuration []byte
	transport := func(ctx context.Context, request githubsetup.Request) (githubsetup.View, error) {
		if request.Action == "apply" {
			parsed, err := config.ParseGitHubFragment(configuration)
			if err != nil || parsed.GitHubAppID == 0 || len(parsed.GitHubTargets) == 0 {
				return githubsetup.View{}, fmt.Errorf("GitHub setup did not provide valid installation configuration")
			}
			fmt.Fprintln(diagnostics, "Applying installation access to the owned services...")
			if err := store.StopFull(ctx, backend); err != nil {
				return githubsetup.View{}, err
			}
			if err := store.SaveProviderConfiguration("github", configuration); err != nil {
				return githubsetup.View{}, err
			}
			if _, err := store.ReconcileFull(ctx, backend); err != nil {
				return githubsetup.View{}, err
			}
		}
		raw, err := backend.Administration(ctx, map[string]any{"operation": "github_setup", "setup_request": request})
		if err != nil {
			return githubsetup.View{}, err
		}
		var view githubsetup.View
		if json.Unmarshal(raw, &view) != nil || view.SchemaVersion != 1 {
			return view, fmt.Errorf("invalid protected GitHub setup response")
		}
		if view.Phase == "configured" {
			configuration = append(configuration[:0], view.Configuration...)
		}
		return view, nil
	}
	if err := githubsetup.Run(ctx, transport, githubsetup.Options{PersonalProjects: personal, NoBrowser: noBrowser, Input: input, UserTransport: githubUserTransport(backend)}, diagnostics); err != nil {
		return err
	}
	fmt.Fprintln(output, "GitHub integration ready. Repository-linked Projects use installation tokens.")
	return nil
}
