package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"loki/internal/host/githubsetup"
)

func TestGitHubInitialSetupUsesInstallationAccountInsteadOfAppOwner(t *testing.T) {
	h, _ := browserSetupFixture(t)
	begin, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: begin.State, Code: "code"}); err != nil {
		t.Fatal(err)
	}
	h.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/app/installations" {
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		return githubHTTPResponse(r, 200, `[{"id":789,"app_id":123,"account":{"id":84,"login":"Example-Org","type":"Organization"},"repository_selection":"all"}]`), nil
	})
	view, err := h.Handle(t.Context(), githubsetup.Request{Action: "poll"})
	if err != nil || view.Phase != "configured" || view.Account != "example-org" || strings.Join(view.Repositories, ",") != "example-org/*" {
		t.Fatalf("GitHub selection was replaced by App owner: view=%+v err=%v", view, err)
	}
}

func TestGitHubSetupWithoutAccountAddsBrowserSelectedInstallation(t *testing.T) {
	installed := false
	h, base, lookups := configuredGitHubAdditionFixture(t, &installed, "all")
	request := githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"}
	view, err := h.Handle(t.Context(), request)
	if err != nil || view.Phase != "installation" || view.Manifest != nil {
		t.Fatalf("begin=%+v err=%v", view, err)
	}
	if view, err = h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err != nil || view.Phase != "installation" {
		t.Fatalf("existing installation prematurely finished setup: view=%+v err=%v", view, err)
	}
	installed = true
	request.RedirectURL = "http://127.0.0.1:43/callback"
	if _, err = h.Handle(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if view, err = h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err != nil || view.Phase != "configured" || view.Account != "example-org" {
		t.Fatalf("poll=%+v err=%v", view, err)
	}
	if *lookups != 0 {
		t.Fatal("account lookup is unnecessary for browser selection")
	}
	h.Apply = func(ctx context.Context, candidate managedGitHubCandidate) error {
		if !bytes.HasPrefix(candidate.ConfigRaw, base) || strings.Join(candidate.Config.GitHubTargets, ",") != "example-user/repo,example-org/*" {
			t.Fatal("existing repository restrictions were lost")
		}
		commitGitHubAddition(t, h, candidate)
		return nil
	}
	if view, err = h.Handle(t.Context(), githubsetup.Request{Action: "apply"}); err != nil || view.Phase != "ready" {
		t.Fatalf("apply=%+v err=%v", view, err)
	}
	if view, err = h.Handle(t.Context(), request); err != nil || view.Phase != "installation" || view.Manifest != nil {
		t.Fatalf("repeat did not reopen existing App: view=%+v err=%v", view, err)
	}
	if view, err = h.Handle(t.Context(), githubsetup.Request{Action: "finish"}); err != nil || view.Phase != "ready" || view.Account != "example-user, example-org" {
		t.Fatalf("finish=%+v err=%v", view, err)
	}
}

func TestGitHubSetupExistingConfigureFinishesWithoutReapplying(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "changed"}[changed], func(t *testing.T) {
			installed := false
			h, base, _ := configuredGitHubAdditionFixture(t, &installed, "all")
			if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"}); err != nil {
				t.Fatal(err)
			}
			h.Apply = func(context.Context, managedGitHubCandidate) error {
				t.Fatal("existing Configure should not rotate credentials or rewrite restrictions")
				return errors.New("unexpected apply")
			}
			action := "finish"
			if changed {
				action = "poll"
				h.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return githubHTTPResponse(r, 200, `[{"id":456,"app_id":123,"account":{"id":42,"login":"example-user","type":"User"},"repository_selection":"all","updated_at":"2026-10-02T00:00:00Z"}]`), nil
				})
			}
			view, err := h.Handle(t.Context(), githubsetup.Request{Action: action})
			if err != nil || view.Phase != "ready" {
				t.Fatalf("finish=%+v err=%v", view, err)
			}
			after, _, err := loadManagedGitHubPublicConfig(t.Context(), h.Store)
			if err != nil || !bytes.Equal(after, base) {
				t.Fatal("Configure changed local allowlist")
			}
		})
	}
}

func TestGitHubSelectionRejectsAmbiguousChanges(t *testing.T) {
	installed := false
	h, base, _ := configuredGitHubAdditionFixture(t, &installed, "all")
	if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"}); err != nil {
		t.Fatal(err)
	}
	h.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return githubHTTPResponse(r, 200, `[{"id":789,"app_id":123,"account":{"id":84,"login":"example-org","type":"Organization"},"repository_selection":"all"},{"id":790,"app_id":123,"account":{"id":85,"login":"another-org","type":"Organization"},"repository_selection":"selected"}]`), nil
	})
	if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("ambiguous selection accepted: %v", err)
	}
	after, _, err := loadManagedGitHubPublicConfig(t.Context(), h.Store)
	if err != nil || !bytes.Equal(after, base) {
		t.Fatal("ambiguous selection overwrote existing configuration")
	}
}

func TestGitHubConfigureURLSelectsAnExistingUnchangedInstallation(t *testing.T) {
	installed := true
	h, base, _ := configuredGitHubAdditionFixture(t, &installed, "all")
	if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"}); err != nil {
		t.Fatal(err)
	}
	if view, err := h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err != nil || view.Phase != "installation" {
		t.Fatalf("baseline account was silently connected: view=%+v err=%v", view, err)
	}
	if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "select", InstallationID: 999}); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("foreign installation accepted: %v", err)
	}
	view, err := h.Handle(t.Context(), githubsetup.Request{Action: "select", InstallationID: 789})
	if err != nil || view.Phase != "configured" || view.Account != "example-org" {
		t.Fatalf("existing selection=%+v err=%v", view, err)
	}
	after, _, err := loadManagedGitHubPublicConfig(t.Context(), h.Store)
	if err != nil || !bytes.Equal(after, base) {
		t.Fatal("selection bypassed managed application")
	}
}
