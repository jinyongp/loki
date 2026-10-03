package githubapp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"loki/internal/config"
	githubsetup "loki/internal/integrations/github/setup"
)

func registrationFixture(t *testing.T, failConversion bool) (Setup, *atomic.Int32, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	conversions := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app-manifests/one-time-code/conversions":
			conversions.Add(1)
			if failConversion {
				http.Error(w, "private upstream details", 503)
				return
			}
			if r.Method != "POST" || r.Header.Get("Authorization") != "" {
				t.Error("invalid unauthenticated App conversion")
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 123, "slug": "test-loki", "pem": private})
		case "/app":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Error("App reuse omitted protected authentication")
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 123, "slug": "test-loki"})
		case "/app/installations":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Error("installation discovery omitted protected authentication")
			}
			io := `[ {"id":456,"app_id":123,"repository_selection":"selected","account":{"login":"example-user","type":"User"}}, {"id":789,"app_id":123,"repository_selection":"all","account":{"login":"example-org","type":"Organization"}} ]`
			w.Write([]byte(io))
		default:
			t.Errorf("unexpected API path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return Setup{Credentials: Credentials{StateDirectory: filepath.Join(t.TempDir(), "github")}, APIURL: server.URL, Client: server.Client()}, conversions, private
}

func TestGitHubRegistrationResumesAndReusesInstallationSettings(t *testing.T) {
	s, conversions, private := registrationFixture(t, false)
	begin, err := s.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:12345/callback"})
	if err != nil {
		t.Fatal(err)
	}
	if begin.Manifest == nil || begin.Manifest.DefaultPermissions["issue_fields"] != "write" || begin.Manifest.DefaultPermissions["issue_types"] != "write" || begin.Manifest.DefaultPermissions["organization_projects"] != "write" || begin.Manifest.DefaultPermissions["user_projects"] != "" {
		t.Fatal("default repository setup permission contract changed")
	}
	if _, err := s.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: "foreign", Code: "one-time-code"}); err == nil || conversions.Load() != 0 {
		t.Fatal("foreign callback reached conversion")
	}
	installation, err := s.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: begin.State, Code: "one-time-code"})
	if err != nil || installation.Phase != "installation" {
		t.Fatalf("conversion: %v", err)
	}
	resumed, err := s.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:54321/callback"})
	if err != nil || resumed.Phase != "installation" || conversions.Load() != 1 {
		t.Fatal("resumption converted a second App")
	}
	configured, err := s.Handle(t.Context(), githubsetup.Request{Action: "finish"})
	if err != nil {
		t.Fatal(err)
	}
	if configured.Phase != "configured" || len(configured.Repositories) != 2 {
		t.Fatal("approved installations were not completely reconciled")
	}
	raw, _ := json.Marshal(configured)
	if strings.Contains(string(raw), private) || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatal("private key escaped through a public setup view")
	}
	parsed, err := config.ParseGitHubFragment(configured.Configuration)
	if err != nil || len(parsed.GitHubInstallations) != 2 {
		t.Fatal("public installation configuration is invalid")
	}
	s.Configuration = parsed // Represents the restarted runtime's new layout.
	if _, err := s.Handle(t.Context(), githubsetup.Request{Action: "apply"}); err != nil {
		t.Fatal(err)
	}
	if reused, err := s.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:54321/callback"}); err != nil || reused.Phase != "installation" || conversions.Load() != 1 {
		t.Fatal("existing configured App was not reused")
	}
}

func TestGitHubUncertainConversionCannotReplayAndFileRecoveryClearsIntent(t *testing.T) {
	s, conversions, _ := registrationFixture(t, true)
	begin, err := s.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:12345/callback", PersonalProjects: true})
	if err != nil || begin.Manifest.DefaultPermissions["user_projects"] != "write" {
		t.Fatal("explicit optional account permission was omitted")
	}
	if _, err := s.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: begin.State, Code: "one-time-code"}); err == nil {
		t.Fatal("failed conversion accepted")
	}
	if _, err := s.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: begin.State, Code: "one-time-code"}); err == nil || conversions.Load() != 1 {
		t.Fatal("uncertain one-time conversion was replayed")
	}
	if _, err := s.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:54321/callback"}); err == nil {
		t.Fatal("uncertain registration silently replaced")
	}
	if err := s.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:54321/callback"}); err != nil {
		t.Fatal(err)
	}
}
