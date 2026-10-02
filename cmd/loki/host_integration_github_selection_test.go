package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"loki/internal/config"
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
	view, err := h.Handle(t.Context(), githubsetup.Request{Action: "finish"})
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
	if view, err = h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err != nil || view.Phase != "installation" {
		t.Fatalf("setup ended before browser configuration was complete: view=%+v err=%v", view, err)
	}
	if view, err = h.Handle(t.Context(), githubsetup.Request{Action: "finish"}); err != nil || view.Phase != "configured" || view.Account != "example-user, example-org" {
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

func TestGitHubSetupConnectsAllApprovedInstallationsOnFinish(t *testing.T) {
	installed := false
	h, base, _ := configuredGitHubAdditionFixture(t, &installed, "all")
	if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"}); err != nil {
		t.Fatal(err)
	}
	transport := h.Client.Transport
	h.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/app/installations" {
			return transport.RoundTrip(r)
		}
		return githubHTTPResponse(r, 200, `[{"id":456,"app_id":123,"account":{"id":42,"login":"example-user","type":"User"},"repository_selection":"all"},{"id":789,"app_id":123,"account":{"id":84,"login":"example-org","type":"Organization"},"repository_selection":"all"},{"id":790,"app_id":123,"account":{"id":85,"login":"another-org","type":"Organization"},"repository_selection":"selected"},{"id":791,"app_id":123,"account":{"id":86,"login":"suspended-org","type":"Organization"},"repository_selection":"all","suspended_at":"2026-10-02T00:00:00Z"}]`), nil
	})
	if view, err := h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err != nil || view.Phase != "installation" {
		t.Fatalf("accounts applied before Enter: view=%+v err=%v", view, err)
	}
	if view, err := h.Handle(t.Context(), githubsetup.Request{Action: "finish"}); err != nil || view.Phase != "configured" {
		t.Fatalf("multiple installations rejected: view=%+v err=%v", view, err)
	}
	after, _, err := loadManagedGitHubPublicConfig(t.Context(), h.Store)
	if err != nil || !bytes.Equal(after, base) {
		t.Fatal("finish bypassed the managed transaction")
	}
	applies := 0
	h.Apply = func(ctx context.Context, candidate managedGitHubCandidate) error {
		applies++
		if candidate.Config.GitHubMaxPages != 7 || !bytes.HasPrefix(candidate.ConfigRaw, base) || strings.Join(candidate.Config.GitHubTargets, ",") != "example-user/repo,another-org/*,example-org/*" {
			t.Fatalf("batch lost installations or existing restrictions: %+v", candidate.Config)
		}
		commitGitHubAddition(t, h, candidate)
		return nil
	}
	if view, err := h.Handle(t.Context(), githubsetup.Request{Action: "apply"}); err != nil || view.Phase != "ready" || view.Account != "example-user, another-org, example-org" || applies != 1 {
		t.Fatalf("batch apply=%+v err=%v applies=%d", view, err, applies)
	}
	if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"}); err != nil {
		t.Fatal(err)
	}
	if view, err := h.Handle(t.Context(), githubsetup.Request{Action: "finish"}); err != nil || view.Phase != "ready" || applies != 1 {
		t.Fatalf("unchanged batch reapplied: view=%+v err=%v applies=%d", view, err, applies)
	}
}

func TestGitHubSetupConnectsPreviouslyInstalledAccountsWithoutMetadataChanges(t *testing.T) {
	installed := true
	h, base, _ := configuredGitHubAdditionFixture(t, &installed, "all")
	if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"}); err != nil {
		t.Fatal(err)
	}
	view, err := h.Handle(t.Context(), githubsetup.Request{Action: "finish"})
	if err != nil || view.Phase != "configured" || strings.Join(view.Repositories, ",") != "example-user/repo,example-org/*" {
		t.Fatalf("existing installations were omitted: view=%+v err=%v", view, err)
	}
	after, _, err := loadManagedGitHubPublicConfig(t.Context(), h.Store)
	if err != nil || !bytes.Equal(after, base) {
		t.Fatal("finish bypassed managed application")
	}
}

func TestGitHubSetupResumesLegacyPendingInstallationAsBatch(t *testing.T) {
	installed := false
	h, _, _ := configuredGitHubAdditionFixture(t, &installed, "all")
	if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback", Account: "example-org"}); err != nil {
		t.Fatal(err)
	}
	view, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:43/callback"})
	if err != nil || !view.RequireConfirmation || view.Phase != "installation" {
		t.Fatalf("legacy pending setup retained automatic completion: view=%+v err=%v", view, err)
	}
	installed = true
	if view, err := h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err != nil || view.Phase != "installation" {
		t.Fatalf("legacy setup ended before Enter: view=%+v err=%v", view, err)
	}
	if view, err := h.Handle(t.Context(), githubsetup.Request{Action: "finish"}); err != nil || view.Phase != "configured" || view.Account != "example-user, example-org" {
		t.Fatalf("legacy pending setup did not collect accounts: view=%+v err=%v", view, err)
	}
}

func TestGitHubSetupBatchRejectsInvalidOrOverLimitInstallationsAtomically(t *testing.T) {
	for _, scenario := range []string{"foreign-app", "duplicate", "identity-changed", "suspended-existing", "too-many", "incomplete-list"} {
		t.Run(scenario, func(t *testing.T) {
			installed := false
			h, base, _ := configuredGitHubAdditionFixture(t, &installed, "all")
			if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"}); err != nil {
				t.Fatal(err)
			}
			count := 1
			if scenario == "too-many" {
				count = 16
			}
			if scenario == "incomplete-list" {
				count = 100
			}
			h.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				page, err := strconv.Atoi(r.URL.Query().Get("page"))
				if err != nil {
					t.Fatal(err)
				}
				items := make([]setupInstallation, count)
				for i := range items {
					item := &items[i]
					index := (page-1)*count + i
					item.ID, item.AppID, item.Account.ID = int64(789+index), 123, int64(84+index)
					item.Account.Login, item.Account.Type, item.Selection = fmt.Sprintf("org-%d", index), "Organization", "all"
				}
				switch scenario {
				case "foreign-app":
					items[0].AppID = 999
				case "duplicate":
					items = append(items, items[0])
				case "identity-changed":
					items[0].ID = 456
				case "suspended-existing":
					items[0].ID, items[0].Account.Login, items[0].Account.Type = 456, "example-user", "User"
					stamp := time.Now()
					items[0].SuspendedAt = &stamp
				}
				raw, err := json.Marshal(items)
				if err != nil {
					t.Fatal(err)
				}
				return githubHTTPResponse(r, 200, string(raw)), nil
			})
			if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "finish"}); err == nil {
				t.Fatal("invalid installation batch accepted")
			}
			after, _, err := loadManagedGitHubPublicConfig(t.Context(), h.Store)
			if err != nil || !bytes.Equal(after, base) {
				t.Fatal("failed batch changed existing configuration")
			}
			raw, err := h.Store.ReadGitHubSetup(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer clear(raw)
			var saved githubSetupSession
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			defer clear(saved.PrivateKey)
			if saved.Phase != "installation" || len(saved.ConfigRaw) != 0 {
				t.Fatal("failed batch persisted a partial candidate")
			}
		})
	}
}

func TestGitHubInitialSetupBatchIncludesPersonalAndOrganizationInstallations(t *testing.T) {
	h, _ := browserSetupFixture(t)
	begin, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: begin.State, Code: "code"}); err != nil {
		t.Fatal(err)
	}
	h.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return githubHTTPResponse(r, 200, `[{"id":456,"app_id":123,"account":{"id":42,"login":"example-user","type":"User"},"repository_selection":"selected"},{"id":789,"app_id":123,"account":{"id":84,"login":"example-org","type":"Organization"},"repository_selection":"all"}]`), nil
	})
	if view, err := h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err != nil || view.Phase != "installation" {
		t.Fatalf("initial setup ended early: view=%+v err=%v", view, err)
	}
	if view, err := h.Handle(t.Context(), githubsetup.Request{Action: "finish"}); err != nil || view.Phase != "configured" {
		t.Fatalf("finish=%+v err=%v", view, err)
	}
	h.Apply = func(ctx context.Context, candidate managedGitHubCandidate) error {
		parsed, err := config.ParseGitHubFragment(candidate.ConfigRaw)
		if err != nil || len(parsed.GitHubInstallations) != 2 {
			t.Fatalf("initial setup omitted installation: config=%+v err=%v", parsed, err)
		}
		return nil
	}
	if view, err := h.Handle(t.Context(), githubsetup.Request{Action: "apply"}); err != nil || view.Account != "example-org, example-user" {
		t.Fatalf("ready=%+v err=%v", view, err)
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
	if err != nil || view.Phase != "configured" || view.Account != "example-user, example-org" {
		t.Fatalf("existing selection=%+v err=%v", view, err)
	}
	after, _, err := loadManagedGitHubPublicConfig(t.Context(), h.Store)
	if err != nil || !bytes.Equal(after, base) {
		t.Fatal("selection bypassed managed application")
	}
}
