package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loki/internal/config"
	"loki/internal/host/lifecycle"
	lifecyclecompose "loki/internal/host/lifecycle/compose"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func githubPrivateKeyFixture(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func githubConfigFixture() []byte {
	return []byte(`github_app_id = 123
github_api_version = "2026-03-10"

[[github_installations]]
account = "example-org"
account_type = "organization"
installation_id = 456
repositories = ["repo"]
`)
}

func managedGitHubCandidateFixture(t *testing.T) managedGitHubCandidate {
	t.Helper()
	raw := githubConfigFixture()
	parsed, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return managedGitHubCandidate{ConfigRaw: raw, KeyRaw: githubPrivateKeyFixture(t), Config: parsed}
}

func TestManagedGitHubStdinEnvelopeDecodesWithoutFilePaths(t *testing.T) {
	candidate := managedGitHubCandidateFixture(t)
	defer clear(candidate.KeyRaw)
	envelope, err := json.Marshal(managedGitHubSetupEnvelope{
		Config: candidate.ConfigRaw, PrivateKey: candidate.KeyRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := readManagedGitHubCandidateEnvelope(t.Context(), bytes.NewReader(envelope))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(decoded.KeyRaw)
	if decoded.Config.GitHubAppID != candidate.Config.GitHubAppID ||
		!bytes.Equal(decoded.ConfigRaw, candidate.ConfigRaw) ||
		!bytes.Equal(decoded.KeyRaw, candidate.KeyRaw) {
		t.Fatalf("decoded candidate=%#v", decoded.Config)
	}
	bad := append(append([]byte(nil), envelope...), []byte("\n{}")...)
	if _, err = readManagedGitHubCandidateEnvelope(t.Context(), bytes.NewReader(bad)); err == nil {
		t.Fatal("GitHub stdin envelope accepted trailing data")
	}
}

func TestValidateManagedGitHubCandidateUsesInstallationTokenAndRepositoryRead(t *testing.T) {
	candidate := managedGitHubCandidateFixture(t)
	defer clear(candidate.KeyRaw)
	var tokenCalls atomic.Int32
	var repoCalls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/app/installations/456/access_tokens":
			tokenCalls.Add(1)
			if request.Method != http.MethodPost || !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
				t.Fatalf("token request=%s auth=%q", request.Method, request.Header.Get("Authorization"))
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(body, []byte(`"repositories":["repo"]`)) {
				t.Fatalf("token scope=%s", body)
			}
			return githubHTTPResponse(request, http.StatusCreated,
				`{"token":"installation-token","expires_at":"`+time.Now().UTC().Add(time.Hour).Format(time.RFC3339)+`"}`), nil
		case "/repos/example-org/repo":
			repoCalls.Add(1)
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer installation-token" {
				t.Fatalf("repository request=%s auth=%q", request.Method, request.Header.Get("Authorization"))
			}
			return githubHTTPResponse(request, http.StatusOK,
				`{"full_name":"example-org/repo","default_branch":"main","html_url":"https://github.com/example-org/repo","visibility":"private","archived":false}`), nil
		default:
			t.Fatalf("unexpected GitHub request %s", request.URL)
			return nil, nil
		}
	})}
	if err := validateManagedGitHubCandidate(t.Context(), candidate, client); err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 1 || repoCalls.Load() != 1 {
		t.Fatalf("GitHub validation calls token=%d repo=%d", tokenCalls.Load(), repoCalls.Load())
	}
}

func githubHTTPResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestGitHubPrivateKeyImportRequiresPrivateRegularFile(t *testing.T) {
	root := t.TempDir()
	keyPath := filepath.Join(root, "app.pem")
	key := githubPrivateKeyFixture(t)
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := readGitHubImportFile(t.Context(), keyPath, len(key)+1, true)
	if err != nil || !bytes.Equal(raw, key) {
		t.Fatalf("private key import=%q err=%v", raw, err)
	}
	clear(raw)

	if err = os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = readGitHubImportFile(t.Context(), keyPath, len(key)+1, true); err == nil {
		t.Fatal("world-readable GitHub private key was accepted")
	}
	if err = os.Chmod(keyPath, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "app-link.pem")
	if err = os.Symlink(keyPath, link); err != nil {
		t.Fatal(err)
	}
	if _, err = readGitHubImportFile(t.Context(), link, len(key)+1, true); err == nil {
		t.Fatal("symlink GitHub private key was accepted")
	}
}

func TestInspectHostGitHubIntegrationUsesManagedPublicState(t *testing.T) {
	store, now := hostIntegrationStoreFixture(t)
	candidate := managedGitHubCandidateFixture(t)
	defer clear(candidate.KeyRaw)
	configDigest, err := store.WriteManagedIntegrationFile(t.Context(), lifecycle.ManagedGitHubConfigFile, candidate.ConfigRaw)
	if err != nil {
		t.Fatal(err)
	}
	credentialDigest, err := store.WriteManagedIntegrationFile(t.Context(), lifecycle.ManagedGitHubCredentialFile, candidate.KeyRaw)
	if err != nil {
		t.Fatal(err)
	}
	managed := lifecycle.DefaultManagedIntegrationState()
	managed.GitHub = lifecycle.ManagedIntegrationToggle{
		Configured: true, Enabled: true,
		ConfigSHA256: configDigest, CredentialSHA256: credentialDigest,
	}
	if err = store.CommitManagedIntegrations(t.Context(), managed, "github-ready", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runtime := staticIntegrationRuntime{readiness: lifecyclecompose.RuntimeReadiness{
		Activated: true, GenerationID: "generation",
	}}
	report, err := inspectHostIntegration(t.Context(), store, runtime, "github")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Configured || !report.Enabled || !report.Ready || report.State != "ready" ||
		report.GitHubAppID != 123 || report.TargetCount != 1 ||
		report.Authentication != "GitHub App installation tokens" {
		t.Fatalf("GitHub report=%#v", report)
	}

	managed.GitHub.Enabled = false
	if err = store.CommitManagedIntegrations(t.Context(), managed, "github-disabled", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	report, err = inspectHostIntegration(t.Context(), store, runtime, "github")
	if err != nil {
		t.Fatal(err)
	}
	if report.Enabled || report.Ready || report.State != "disabled" {
		t.Fatalf("disabled GitHub report=%#v", report)
	}
}

func TestManagedGitHubConfigAndCredentialStateContainNoPrivateBytes(t *testing.T) {
	store, _ := hostIntegrationStoreFixture(t)
	candidate := managedGitHubCandidateFixture(t)
	defer clear(candidate.KeyRaw)
	configDigest, err := store.WriteManagedIntegrationFile(t.Context(), lifecycle.ManagedGitHubConfigFile, candidate.ConfigRaw)
	if err != nil {
		t.Fatal(err)
	}
	credentialDigest, err := store.WriteManagedIntegrationFile(t.Context(), lifecycle.ManagedGitHubCredentialFile, candidate.KeyRaw)
	if err != nil {
		t.Fatal(err)
	}
	state := lifecycle.DefaultManagedIntegrationState()
	state.GitHub = lifecycle.ManagedIntegrationToggle{Configured: true, Enabled: true, ConfigSHA256: configDigest, CredentialSHA256: credentialDigest}
	stateRaw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateRaw, candidate.KeyRaw) || bytes.Contains(candidate.ConfigRaw, candidate.KeyRaw) {
		t.Fatal("managed GitHub public state leaked private key bytes")
	}
}
