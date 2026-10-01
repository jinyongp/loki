package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"loki/internal/host/githubsetup"
	"loki/internal/host/lifecycle"
)

func browserSetupFixture(t *testing.T, ownerTypes ...string) (*hostGitHubSetup, *atomic.Int32) {
	ownerType := "User"
	if len(ownerTypes) > 0 {
		ownerType = ownerTypes[0]
	}
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := lifecycle.OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	conversions := new(atomic.Int32)
	key := githubPrivateKeyFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/app-manifests/"):
			conversions.Add(1)
			if r.Header.Get("Authorization") != "" {
				t.Error("conversion must not borrow ambient authentication")
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 123, "slug": "loki-test", "pem": string(key), "owner": map[string]any{"id": 42, "login": "example", "type": ownerType}})
		case r.URL.Path == "/app/installations":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				t.Error("installation discovery requires App JWT")
			}
			fmt.Fprintf(w, `[{"id":456,"app_id":123,"account":{"id":42,"login":"example","type":%q},"repository_selection":"selected","suspended_at":null}]`, ownerType)
		case r.URL.Path == "/app/installations/456/access_tokens":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, "invalid token request", 400)
				return
			}
			permissions, _ := body["permissions"].(map[string]any)
			if len(permissions) != 1 || permissions["metadata"] != "read" {
				t.Error("discovery token must have metadata read only")
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"token":"discovery-token"}`)
		case r.URL.Path == "/installation/repositories":
			if r.Header.Get("Authorization") != "Bearer discovery-token" {
				t.Error("repository discovery uses installation token")
			}
			fmt.Fprint(w, `{"total_count":2,"repositories":[{"name":"RepoB","owner":{"id":42,"login":"example"}},{"name":"RepoA","owner":{"id":42,"login":"example"}}]}`)
		case r.URL.Path == "/installation/token":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected API request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return &hostGitHubSetup{Store: store, Client: server.Client(), APIURL: server.URL, Ready: func(context.Context) (bool, error) { return true, nil }}, conversions
}
func TestHostGitHubBrowserFlowResumesWithoutExposingPrivateKey(t *testing.T) {
	h, conversions := browserSetupFixture(t)
	ctx := context.Background()
	begin, err := h.Handle(ctx, githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:12345/callback"})
	if err != nil {
		t.Fatal(err)
	}
	if begin.Phase != "registration" || begin.Manifest.Public || begin.Manifest.DefaultPermissions["issues"] != "write" {
		t.Fatalf("begin=%#v", begin)
	}
	if _, err = h.Handle(ctx, githubsetup.Request{Action: "exchange", State: "wrong", Code: "validcode"}); err == nil || conversions.Load() != 0 {
		t.Fatal("invalid state reached GitHub")
	}
	installation, err := h.Handle(ctx, githubsetup.Request{Action: "exchange", State: begin.State, Code: "validcode"})
	if err != nil || installation.Phase != "installation" {
		t.Fatalf("exchange=%#v %v", installation, err)
	}
	public, _ := json.Marshal(installation)
	if strings.Contains(string(public), "PRIVATE KEY") || strings.Contains(string(public), "private_key") {
		t.Fatal("private key leaked into public setup view")
	}
	info, err := os.Stat(filepath.Join(h.Store.Root, "github-setup", "session.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("pending private state is not protected")
	}
	resumed, err := h.Handle(ctx, githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:23456/callback"})
	if err != nil || resumed.Phase != "installation" || conversions.Load() != 1 {
		t.Fatalf("resume=%#v %v conversions=%d", resumed, err, conversions.Load())
	}
	// A repeated callback after a committed conversion reuses the saved App.
	if _, err = h.Handle(ctx, githubsetup.Request{Action: "exchange", State: begin.State, Code: "validcode"}); err != nil || conversions.Load() != 1 {
		t.Fatal("conversion replay created another App")
	}
	configured, err := h.Handle(ctx, githubsetup.Request{Action: "poll"})
	if err != nil || configured.Phase != "configured" || strings.Join(configured.Repositories, ",") != "example/repoa,example/repob" {
		t.Fatalf("poll=%#v %v", configured, err)
	}
	h.Apply = func(ctx context.Context, candidate managedGitHubCandidate) error {
		if candidate.Config.GitHubAppID != 123 || len(candidate.Config.GitHubTargets) != 2 || len(candidate.KeyRaw) == 0 {
			t.Fatal("invalid apply candidate")
		}
		return nil
	}
	ready, err := h.Handle(ctx, githubsetup.Request{Action: "apply"})
	if err != nil || ready.Phase != "ready" {
		t.Fatalf("apply=%#v %v", ready, err)
	}
	raw, err := h.Store.ReadGitHubSetup(ctx)
	if err != nil || len(raw) != 0 {
		t.Fatal("successful setup retained pending key copy")
	}
}
func TestGitHubBrowserBeginRequiresLoopbackAndKnownAccountType(t *testing.T) {
	for _, input := range []githubsetup.Request{
		{RedirectURL: "https://example.com/callback"},
		{RedirectURL: "http://127.0.0.1/callback"},
		{RedirectURL: "http://127.0.0.1:42/callback?secret=x"},
		{RedirectURL: "http://user@127.0.0.1:42/callback"},
		{RedirectURL: "http://127.0.0.1:42/callback", AccountType: "organization"},
		{RedirectURL: "http://127.0.0.1:42/callback", Account: "../evil"},
		{RedirectURL: "http://127.0.0.1:42/callback", AccountType: "other"},
	} {
		if err := validateGitHubSetupBegin(input); err == nil {
			t.Fatalf("accepted %#v", input)
		}
	}
}
func TestGitHubConversionUncertaintyDoesNotRepeatSideEffect(t *testing.T) {
	h, count := browserSetupFixture(t)
	ctx := context.Background()
	begin, err := h.Handle(ctx, githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"})
	if err != nil {
		t.Fatal(err)
	}
	h.APIURL = "http://127.0.0.1:1"
	if _, err = h.Handle(ctx, githubsetup.Request{Action: "exchange", State: begin.State, Code: "code"}); err == nil {
		t.Fatal("expected conversion failure")
	}
	if _, err = h.Handle(ctx, githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"}); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("resume must preserve uncertain side effect: %v", err)
	}
	if count.Load() != 0 {
		t.Fatal("unexpected conversion")
	}
}
func TestGitHubDiscoveryRejectsBroaderOrIncompleteRepositorySelection(t *testing.T) {
	for _, test := range []struct{ name, installation, repos string }{
		{"all", `[{"id":456,"app_id":123,"account":{"id":42,"login":"example"},"repository_selection":"all"}]`, ""},
		{"too-many", `[{"id":456,"app_id":123,"account":{"id":42,"login":"example"},"repository_selection":"selected"}]`, `{"total_count":65,"repositories":[]}`},
		{"incomplete", `[{"id":456,"app_id":123,"account":{"id":42,"login":"example"},"repository_selection":"selected"}]`, `{"total_count":2,"repositories":[]}`},
		{"other-owner", `[{"id":456,"app_id":123,"account":{"id":42,"login":"example"},"repository_selection":"selected"}]`, `{"total_count":1,"repositories":[{"name":"repo","owner":{"id":99,"login":"other"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, _ := browserSetupFixture(t)
			ctx := context.Background()
			begin, err := h.Handle(ctx, githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.Handle(ctx, githubsetup.Request{Action: "exchange", State: begin.State, Code: "code"}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/app/installations":
					fmt.Fprint(w, test.installation)
				case "/app/installations/456/access_tokens":
					w.WriteHeader(201)
					fmt.Fprint(w, `{"token":"token"}`)
				case "/installation/repositories":
					fmt.Fprint(w, test.repos)
				case "/installation/token":
					w.WriteHeader(204)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			h.APIURL = server.URL
			if _, err = h.Handle(ctx, githubsetup.Request{Action: "poll"}); err == nil {
				t.Fatal("unsafe or incomplete selection accepted")
			}
		})
	}
}
func TestGitHubBrowserRequestRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for _, input := range []string{`{"action":"begin","private_key":"secret"}`, `{"action":"poll"} {}`, strings.Repeat("x", 8193)} {
		if _, err := readGitHubBrowserRequest(strings.NewReader(input)); err == nil {
			t.Fatal("invalid browser request accepted")
		}
	}
}

func TestHostGitHubBrowserSetupOrganizationAndRegistrationRestart(t *testing.T) {
	h, count := browserSetupFixture(t, "Organization")
	request := githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback", Account: "example", AccountType: "organization"}
	first, err := h.Handle(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.RedirectURL = "http://127.0.0.1:43/callback"
	second, err := h.Handle(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != second.State || first.Manifest.Name != second.Manifest.Name || second.Manifest.RedirectURL != request.RedirectURL || !strings.HasPrefix(second.RegistrationURL, "https://github.com/organizations/example/settings/apps/new?") {
		t.Fatal("registration restart changed App identity or owner")
	}
	if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: first.State, Code: "code"}); err != nil {
		t.Fatal(err)
	}
	configured, err := h.Handle(t.Context(), githubsetup.Request{Action: "poll"})
	if err != nil || configured.Phase != "configured" || count.Load() != 1 {
		t.Fatalf("configured=%+v err=%v", configured, err)
	}
	raw, err := h.Store.ReadGitHubSetup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var saved githubSetupSession
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.AccountType != "organization" {
		t.Fatal("organization owner lost")
	}
	clear(raw)
	clear(saved.PrivateKey)
}

func TestHostGitHubBrowserApplyFailureKeepsPrivateStateAndDoesNotClaimReady(t *testing.T) {
	for _, kind := range []string{"apply", "readiness"} {
		t.Run(kind, func(t *testing.T) {
			h, _ := browserSetupFixture(t)
			begin, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: begin.State, Code: "code"}); err != nil {
				t.Fatal(err)
			}
			if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err != nil {
				t.Fatal(err)
			}
			h.Apply = func(context.Context, managedGitHubCandidate) error {
				if kind == "apply" {
					return errors.New("transaction failed")
				}
				return nil
			}
			h.Ready = func(context.Context) (bool, error) { return false, nil }
			view, err := h.Handle(t.Context(), githubsetup.Request{Action: "apply"})
			if err == nil || view.Phase == "ready" {
				t.Fatal("failed apply reported ready")
			}
			resumed, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:43/callback"})
			if err != nil || resumed.Phase != "configured" {
				t.Fatal("failed apply did not preserve resumable state")
			}
			raw, err := h.Store.ReadGitHubSetup(t.Context())
			if err != nil || len(raw) == 0 {
				t.Fatal("failed apply discarded pending key")
			}
			clear(raw)
		})
	}
}
func TestRemoveUnconfiguredGitHubClearsPendingSetup(t *testing.T) {
	h, _ := browserSetupFixture(t)
	if err := h.Store.WriteGitHubSetup(t.Context(), []byte("private pending state")); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedGitHub(t.Context(), lifecycle.Manager{}, h.Store, lifecycle.MutationOptions{}); err != nil {
		t.Fatal(err)
	}
	raw, err := h.Store.ReadGitHubSetup(t.Context())
	if err != nil || len(raw) != 0 {
		t.Fatal("remove retained pending setup")
	}
}
func TestGitHubBrowserCLIRejectsUnavailableHostBeforeRegistration(t *testing.T) {
	h, count := browserSetupFixture(t)
	var out, stderr strings.Builder
	if code := runHostGitHubBrowserSetup(hostIntegrationOptions{}, h.Store, true, githubsetup.Options{}, &out, &stderr); code != 1 {
		t.Fatal("uninstalled host accepted")
	}
	if out.Len() != 0 || count.Load() != 0 || !strings.Contains(stderr.String(), "installation is unavailable") {
		t.Fatal("uninstalled host created an App")
	}
}
