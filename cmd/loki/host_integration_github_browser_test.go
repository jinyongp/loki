package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

type cancelConversionBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body cancelConversionBody) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

func TestGitHubConversionPersistsKnownAppAfterRequestCancellation(t *testing.T) {
	h, count := browserSetupFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	begin, err := h.Handle(ctx, githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"})
	if err != nil {
		t.Fatal(err)
	}
	transport := h.Client.Transport
	h.Client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if err == nil && strings.HasPrefix(request.URL.Path, "/app-manifests/") {
			response.Body = cancelConversionBody{ReadCloser: response.Body, cancel: cancel}
		}
		return response, err
	})
	_, _ = h.Handle(ctx, githubsetup.Request{Action: "exchange", State: begin.State, Code: "code"})
	if ctx.Err() == nil {
		t.Fatal("fixture did not cancel after the conversion response")
	}
	resumed, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:43/callback"})
	if err != nil || resumed.Phase != "installation" || count.Load() != 1 {
		t.Fatalf("known App was lost on cancellation: phase=%s conversions=%d err=%v", resumed.Phase, count.Load(), err)
	}
}

func TestGitHubSetupResumePreservesExplicitOwnerType(t *testing.T) {
	for _, phase := range []string{"installation", "configured"} {
		t.Run(phase, func(t *testing.T) {
			h, _ := browserSetupFixture(t, "Organization")
			begin, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback", Account: "example", AccountType: "organization"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: begin.State, Code: "code"}); err != nil {
				t.Fatal(err)
			}
			if phase == "configured" {
				if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "poll"}); err != nil {
					t.Fatal(err)
				}
			}
			request := githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:43/callback", Account: "example", AccountType: "user"}
			if _, err = h.Handle(t.Context(), request); err == nil {
				t.Fatal("resume ignored conflicting explicit account type")
			}
			request.Account = ""
			resumed, err := h.Handle(t.Context(), request)
			if err != nil || resumed.Phase != phase || resumed.Account != "example" {
				t.Fatalf("bare resume lost organization App: view=%+v err=%v", resumed, err)
			}
		})
	}
}

func TestGitHubDiscoveryAcceptsExistingRepositoryNameContract(t *testing.T) {
	h, _ := browserSetupFixture(t)
	begin, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: begin.State, Code: "code"}); err != nil {
		t.Fatal(err)
	}
	transport := h.Client.Transport
	h.Client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/installation/repositories" {
			return githubHTTPResponse(request, http.StatusOK, `{"total_count":2,"repositories":[{"name":"-repo","owner":{"id":42,"login":"example"}},{"name":"repo-","owner":{"id":42,"login":"example"}}]}`), nil
		}
		return transport.RoundTrip(request)
	})
	configured, err := h.Handle(t.Context(), githubsetup.Request{Action: "poll"})
	if err != nil || configured.Phase != "configured" || strings.Join(configured.Repositories, ",") != "example/-repo,example/repo-" {
		t.Fatalf("existing repository names rejected: view=%+v err=%v", configured, err)
	}
}

func TestGitHubSetupResumesInterruptedLifecycleApply(t *testing.T) {
	for _, phase := range []lifecycle.OperationPhase{lifecycle.PhaseMigrate, lifecycle.PhaseHealth} {
		t.Run(string(phase), func(t *testing.T) {
			h, _ := browserSetupFixture(t)
			now := time.Now().UTC()
			generation := hostGenerationFixture(t, now)
			if err := h.Store.InitializeInstall(t.Context(), generation, lifecycle.InstallationState{Scope: "user", Workspace: t.TempDir()}, now); err != nil {
				t.Fatal(err)
			}
			if err := h.Store.CommitGeneration(t.Context(), generation, now); err != nil {
				t.Fatal(err)
			}
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
			backend := &fakeHostRuntimeBackend{active: generation.ID}
			snapshot, err := h.Store.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := backend.Snapshot(t.Context(), lifecycle.OperationUpdateIntegration, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			runtime.ManagedIntegrationRef, err = h.Store.CaptureManagedIntegrationSnapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			backup, err := lifecycle.NewBackupRecord(lifecycle.OperationUpdateIntegration, snapshot, runtime, now)
			if err != nil {
				t.Fatal(err)
			}
			if err = h.Store.SaveBackup(t.Context(), backup); err != nil {
				t.Fatal(err)
			}
			plan, err := lifecycle.Prepare(snapshot.Installed, *snapshot.Installed, snapshot.Host, now)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := lifecycle.AcquireOperationLock(h.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = lock.Close() })
			journal, err := lifecycle.OpenOperationJournal(h.Store.Root, lock, lifecycle.OperationJournalOptions{})
			if err != nil {
				t.Fatal(err)
			}
			record, err := journal.Begin(lifecycle.OperationUpdateIntegration, plan, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = journal.RecordSnapshot(record.ID, backup.ID, now); err != nil {
				t.Fatal(err)
			}
			raw, err := h.Store.ReadGitHubSetup(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var saved githubSetupSession
			if err = json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			clear(raw)
			defer clear(saved.PrivateKey)
			commit := func(ctx context.Context, store *lifecycle.FileStore, configRaw, keyRaw []byte) error {
				configDigest, err := store.WriteManagedIntegrationFile(ctx, lifecycle.ManagedGitHubConfigFile, configRaw)
				if err != nil {
					return err
				}
				keyDigest, err := store.WriteManagedIntegrationFile(ctx, lifecycle.ManagedGitHubCredentialFile, keyRaw)
				if err != nil {
					return err
				}
				state, err := store.ReadManagedIntegrations(ctx)
				if err != nil {
					return err
				}
				state.GitHub = lifecycle.ManagedIntegrationToggle{Configured: true, Enabled: true, ConfigSHA256: configDigest, CredentialSHA256: keyDigest}
				return store.CommitManagedIntegrations(ctx, state, "setup-github", time.Now().UTC())
			}
			if err = commit(t.Context(), h.Store, saved.ConfigRaw, saved.PrivateKey); err != nil {
				t.Fatal(err)
			}
			for _, next := range []lifecycle.OperationPhase{lifecycle.PhaseSwitch, lifecycle.PhaseMigrate, lifecycle.PhaseRestart, lifecycle.PhaseHealth} {
				if _, err = journal.Advance(record.ID, next, now); err != nil {
					t.Fatal(err)
				}
				if next == phase {
					break
				}
			}
			if phase == lifecycle.PhaseMigrate {
				if err = backend.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if err = lock.Close(); err != nil {
				t.Fatal(err)
			}
			// Atomic journal publication can leave a safe temporary file after a kill.
			if err = os.WriteFile(filepath.Join(h.Store.Root, "operations", ".loki-private-interrupted"), []byte("partial operation record"), 0600); err != nil {
				t.Fatal(err)
			}
			engine := &lifecycle.TransactionEngine{Store: h.Store, Backend: backend}
			manager := lifecycle.Manager{Store: h.Store, Jobs: staticHostJobs{jobs: []string{"job-active"}}, Maintainer: engine}
			h.Reconcile = func(ctx context.Context) error {
				return manager.RecoverManagedIntegration(ctx, lifecycle.MutationOptions{})
			}
			var blocked *lifecycle.BlockedJobsError
			if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:43/callback"}); !errors.As(err, &blocked) {
				t.Fatalf("recovery bypassed active-job policy: %v", err)
			}
			h.Reconcile = func(ctx context.Context) error {
				return manager.RecoverManagedIntegration(ctx, lifecycle.MutationOptions{InterruptActiveJobs: true})
			}
			h.Ready = func(ctx context.Context) (bool, error) {
				state, err := h.Store.ReadManagedIntegrations(ctx)
				return state.GitHub.Configured && backend.active != "", err
			}
			h.Apply = func(ctx context.Context, candidate managedGitHubCandidate) error {
				return engine.UpdateManagedIntegration(ctx, "github", func(ctx context.Context, store *lifecycle.FileStore) error {
					return commit(ctx, store, candidate.ConfigRaw, candidate.KeyRaw)
				})
			}
			resumed, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:43/callback"})
			if err != nil || resumed.Phase != "configured" {
				t.Fatalf("interrupted apply did not resume its saved App: phase=%s err=%v", resumed.Phase, err)
			}
			state, err := h.Store.ReadManagedIntegrations(t.Context())
			if err != nil || state.GitHub.Configured {
				t.Fatal("interrupted transaction was not recovered before readiness")
			}
			operations, err := lifecycle.ReadOperationSnapshot(h.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range operations {
				if operation.ID == record.ID && operation.State != lifecycle.OperationRolledBack {
					t.Fatal("unfinished apply was not rolled back")
				}
			}
			ready, err := h.Handle(t.Context(), githubsetup.Request{Action: "apply"})
			if err != nil || ready.Phase != "ready" {
				t.Fatalf("saved App could not be reapplied: phase=%s err=%v", ready.Phase, err)
			}
			raw, err = h.Store.ReadGitHubSetup(t.Context())
			if err != nil || len(raw) != 0 {
				t.Fatal("successful reapply retained pending key")
			}
			manager.Jobs = staticHostJobs{err: errors.New("a committed integration must not query jobs for recovery")}
			if err = manager.RecoverManagedIntegration(t.Context(), lifecycle.MutationOptions{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
