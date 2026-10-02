package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"loki/internal/config"
	"loki/internal/host/githubsetup"
	"loki/internal/host/lifecycle"
)

func configuredGitHubAdditionFixture(t *testing.T, installed *bool, selection string) (*hostGitHubSetup, []byte, *int) {
	t.Helper()
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
	base := []byte("github_app_id = 123\ngithub_max_pages = 7\n[[github_installations]]\naccount = \"example-user\"\naccount_type = \"user\"\ninstallation_id = 456\nrepositories = [\"repo\"]\n")
	configDigest, err := h.Store.WriteManagedIntegrationFile(ctx, lifecycle.ManagedGitHubConfigFile, base)
	if err != nil {
		t.Fatal(err)
	}
	key := githubPrivateKeyFixture(t)
	defer clear(key)
	keyDigest, err := h.Store.WriteManagedIntegrationFile(ctx, lifecycle.ManagedGitHubCredentialFile, key)
	if err != nil {
		t.Fatal(err)
	}
	state := lifecycle.DefaultManagedIntegrationState()
	state.GitHub = lifecycle.ManagedIntegrationToggle{Configured: true, Enabled: true, ConfigSHA256: configDigest, CredentialSHA256: keyDigest}
	if err = h.Store.CommitManagedIntegrations(ctx, state, "test-setup", now); err != nil {
		t.Fatal(err)
	}
	lookups := new(int)
	h.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/users/example-org":
			*lookups++
			return githubHTTPResponse(r, 200, `{"id":84,"login":"Example-Org","type":"Organization"}`), nil
		case "/app":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				t.Fatal("existing App metadata was read without App authentication")
			}
			return githubHTTPResponse(r, 200, `{"id":123,"slug":"existing-app","owner":{"id":42,"login":"example-user","type":"User"}}`), nil
		case "/app/installations":
			if conversions.Load() != 0 {
				t.Fatal("adding an account created a replacement App")
			}
			body := `[{"id":456,"app_id":123,"account":{"id":42,"login":"example-user","type":"User"},"repository_selection":"selected"}]`
			if *installed {
				body = fmt.Sprintf(`[{"id":456,"app_id":123,"account":{"id":42,"login":"example-user","type":"User"},"repository_selection":"selected"},{"id":789,"app_id":123,"account":{"id":84,"login":"Example-Org","type":"Organization"},"repository_selection":%q}]`, selection)
			}
			return githubHTTPResponse(r, 200, body), nil
		default:
			t.Fatalf("unexpected API request %s", r.URL.Path)
			return nil, errors.New("unexpected API request")
		}
	})
	return h, base, lookups
}

func commitGitHubAddition(t *testing.T, h *hostGitHubSetup, candidate managedGitHubCandidate) {
	t.Helper()
	state, err := h.Store.ReadManagedIntegrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = validateGitHubInstallationAddition(t.Context(), h.Store, state, candidate); err != nil {
		t.Fatal(err)
	}
	state.GitHub.ConfigSHA256, err = h.Store.WriteManagedIntegrationFile(t.Context(), lifecycle.ManagedGitHubConfigFile, candidate.ConfigRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Store.CommitManagedIntegrations(t.Context(), state, "test-addition", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubBrowserAddsOrganizationAndPreservesExistingAccount(t *testing.T) {
	for _, selection := range []string{"selected", "all"} {
		t.Run(selection, func(t *testing.T) {
			installed := false
			h, base, lookups := configuredGitHubAdditionFixture(t, &installed, selection)
			request := githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback", Account: "example-org"}
			view, err := h.Handle(t.Context(), request)
			if err != nil || view.Phase != "installation" || view.Manifest != nil || view.InstallationURL != "https://github.com/apps/existing-app/installations/new" || view.AppSettingsURL == "" {
				t.Fatalf("addition did not reuse existing App: view=%+v err=%v", view, err)
			}
			active, _, err := loadManagedGitHubPublicConfig(t.Context(), h.Store)
			if err != nil || !bytes.Equal(active, base) {
				t.Fatal("pending installation changed the active configuration")
			}
			request.RedirectURL = "http://127.0.0.1:43/callback"
			if view, err = h.Handle(t.Context(), request); err != nil || view.Phase != "installation" || *lookups != 1 {
				t.Fatalf("resume lost target or repeated account lookup: view=%+v err=%v lookups=%d", view, err, *lookups)
			}
			installed = true
			if view, err = h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err != nil || view.Phase != "configured" {
				t.Fatalf("poll=%+v err=%v", view, err)
			}
			applies := 0
			h.Apply = func(ctx context.Context, candidate managedGitHubCandidate) error {
				applies++
				if candidate.Config.GitHubAppID != 123 || candidate.Config.GitHubMaxPages != 7 || strings.Join(candidate.Config.GitHubTargets, ",") != "example-user/repo,example-org/*" {
					t.Fatal("addition changed existing App, limits or repository access")
				}
				if applies == 1 {
					return errors.New("temporary apply failure")
				}
				commitGitHubAddition(t, h, candidate)
				return nil
			}
			if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "apply"}); err == nil {
				t.Fatal("failed apply reported success")
			}
			if view, err = h.Handle(t.Context(), request); err != nil || view.Phase != "configured" {
				t.Fatalf("failed apply did not resume: view=%+v err=%v", view, err)
			}
			if view, err = h.Handle(t.Context(), githubsetup.Request{Action: "apply"}); err != nil || view.Phase != "ready" || view.Account != "example-user, example-org" {
				t.Fatalf("apply=%+v err=%v", view, err)
			}
			if view, err = h.Handle(t.Context(), request); err != nil || view.Phase != "ready" || view.Account != "example-user, example-org" || applies != 2 {
				t.Fatalf("repeated setup changed integration: view=%+v err=%v applies=%d", view, err, applies)
			}
			pending, err := h.Store.ReadGitHubSetup(t.Context())
			if err != nil || len(pending) != 0 {
				t.Fatal("successful addition retained pending credentials")
			}
		})
	}
}

func TestGitHubInstallationAdditionResumesCommittedApply(t *testing.T) {
	installed := true
	h, _, _ := configuredGitHubAdditionFixture(t, &installed, "all")
	request := githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback", Account: "example-org"}
	view, err := h.Handle(t.Context(), request)
	if err != nil || view.Phase != "configured" {
		t.Fatalf("existing installation was not discovered: view=%+v err=%v", view, err)
	}
	raw, err := h.Store.ReadGitHubSetup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	var saved githubSetupSession
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	defer clear(saved.PrivateKey)
	parsed, err := config.ParseGitHubFragment(saved.ConfigRaw)
	if err != nil {
		t.Fatal(err)
	}
	commitGitHubAddition(t, h, managedGitHubCandidate{ConfigRaw: saved.ConfigRaw, KeyRaw: saved.PrivateKey, Config: parsed})
	h.Apply = func(context.Context, managedGitHubCandidate) error {
		t.Fatal("committed addition was applied again")
		return nil
	}
	if view, err = h.Handle(t.Context(), request); err != nil || view.Phase != "ready" || !strings.Contains(view.Account, "example-org") {
		t.Fatalf("committed apply did not recover: view=%+v err=%v", view, err)
	}
}

func TestGitHubInstallationAdditionPreservesConcurrentChanges(t *testing.T) {
	for _, change := range []string{"pending target", "config", "key"} {
		t.Run(change, func(t *testing.T) {
			installed := true
			h, _, _ := configuredGitHubAdditionFixture(t, &installed, "all")
			request := githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback", Account: "example-org"}
			if _, err := h.Handle(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			state, err := h.Store.ReadManagedIntegrations(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "pending target":
				request.Account = "another-org"
			case "config":
				current, _, err := loadManagedGitHubPublicConfig(t.Context(), h.Store)
				if err != nil {
					t.Fatal(err)
				}
				state.GitHub.ConfigSHA256, err = h.Store.WriteManagedIntegrationFile(t.Context(), lifecycle.ManagedGitHubConfigFile, []byte(strings.Replace(string(current), "github_max_pages = 7", "github_max_pages = 8", 1)))
				if err != nil {
					t.Fatal(err)
				}
			case "key":
				state.GitHub.CredentialSHA256 = strings.Repeat("2", 64)
			}
			if err = h.Store.CommitManagedIntegrations(t.Context(), state, "test-change", time.Now()); err != nil {
				t.Fatal(err)
			}
			view, err := h.Handle(t.Context(), request)
			if err == nil || view.Phase == "ready" {
				t.Fatal("concurrent change was overwritten or reported ready")
			}
			after, err := h.Store.ReadManagedIntegrations(t.Context())
			if err != nil || after.GitHub != state.GitHub {
				t.Fatal("rejected addition changed the existing integration")
			}
		})
	}
}
