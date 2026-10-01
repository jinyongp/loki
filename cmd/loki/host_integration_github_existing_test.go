package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"loki/internal/host/githubsetup"
	"loki/internal/host/lifecycle"
)

func TestGitHubConfiguredSetupHonorsRequestedAccount(t *testing.T) {
	for _, test := range []struct {
		name, account, accountType string
		wantError                  string
	}{
		{name: "unspecified"},
		{name: "same account", account: "example"},
		{name: "case insensitive", account: "ExAmPlE", accountType: "user"},
		{name: "another App pending", account: "another-org", wantError: "another GitHub App setup is pending"},
		{name: "different type", account: "example", accountType: "organization", wantError: "requested account type"},
		{name: "invalid account", account: "../invalid", wantError: "GitHub account is invalid"},
		{name: "invalid type", account: "example", accountType: "invalid", wantError: "GitHub account type"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, conversions := browserSetupFixture(t)
			ctx := t.Context()
			now := time.Now().UTC()
			generation := hostGenerationFixture(t, now)
			if err := h.Store.InitializeInstall(ctx, generation, lifecycle.InstallationState{Scope: "user", Workspace: t.TempDir()}, now); err != nil {
				t.Fatal(err)
			}
			if err := h.Store.CommitGeneration(ctx, generation, now); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Handle(ctx, githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"}); err != nil {
				t.Fatal(err)
			}
			pending, err := h.Store.ReadGitHubSetup(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(pending)
			configRaw := []byte("github_app_id = 123\n[[github_installations]]\naccount = \"example\"\naccount_type = \"user\"\ninstallation_id = 456\nrepositories = [\"*\"]\n")
			configDigest, err := h.Store.WriteManagedIntegrationFile(ctx, lifecycle.ManagedGitHubConfigFile, configRaw)
			if err != nil {
				t.Fatal(err)
			}
			state := lifecycle.DefaultManagedIntegrationState()
			state.GitHub = lifecycle.ManagedIntegrationToggle{Configured: true, Enabled: true, ConfigSHA256: configDigest, CredentialSHA256: strings.Repeat("1", 64)}
			if err = h.Store.CommitManagedIntegrations(ctx, state, "test-setup", time.Now()); err != nil {
				t.Fatal(err)
			}
			readyCalls := 0
			h.Ready = func(context.Context) (bool, error) { readyCalls++; return true, nil }
			view, err := h.Handle(ctx, githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:43/callback", Account: test.account, AccountType: test.accountType})
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) || view.Phase == "ready" || readyCalls != 0 {
					t.Fatalf("account mismatch reported ready: view=%+v err=%v calls=%d", view, err, readyCalls)
				}
				after, readErr := h.Store.ReadGitHubSetup(ctx)
				defer clear(after)
				if readErr != nil || string(after) != string(pending) {
					t.Fatal("rejected request changed pending setup")
				}
			} else if err != nil || view.Phase != "ready" || view.Account != "example" || strings.Join(view.Repositories, ",") != "example/*" || readyCalls != 1 {
				t.Fatalf("configured setup omitted account or repository access: view=%+v err=%v calls=%d", view, err, readyCalls)
			}
			if conversions.Load() != 0 {
				t.Fatal("configured setup created another App")
			}
			after, readErr := h.Store.ReadManagedIntegrations(ctx)
			if readErr != nil || after.GitHub != state.GitHub {
				t.Fatal("configured setup changed active integration")
			}
		})
	}
}
