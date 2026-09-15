package githubapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBrokerCommandAndIssueFieldsIntegration(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	exchanges := map[string]int{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/456/access_tokens", "/app/installations/789/access_tokens":
			request := map[string][]string{}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request["repositories"]) != 1 {
				t.Errorf("token request: %#v %v", request, err)
			}
			wantRepository, token := "loki", "token-456"
			if strings.Contains(r.URL.Path, "/789/") {
				wantRepository, token = "personal", "token-789"
			}
			if request["repositories"][0] != wantRepository {
				t.Errorf("token scope: %#v", request)
			}
			mu.Lock()
			exchanges[token]++
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, "{\"token\":\""+token+"\",\"expires_at\":\"2026-09-15T01:00:00Z\"}")
		case "/orgs/connextable/issue-fields":
			if r.Header.Get("Authorization") != "Bearer token-456" {
				t.Error("Issue Fields used the wrong installation token")
			}
			io.WriteString(w, "[]")
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	keyPath := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyPath, []byte(testKey(t, 2048)), 0400); err != nil {
		t.Fatal(err)
	}
	broker := &Broker{
		Config: BrokerConfig{
			AppID: 123, APIVersion: "2026-03-10", MaxResponseBytes: 4096,
			Targets: map[string]Target{
				"connextable/loki":  {InstallationID: 456, Repository: "loki"},
				"jinyongp/personal": {InstallationID: 789, Repository: "personal"},
			},
		},
		Client: server.Client(),
		PrivateKey: func(ctx context.Context) (string, error) {
			return LoadPrivateKeyFile(ctx, keyPath)
		},
		Now: func() time.Time { return now }, apiURL: server.URL,
	}

	binary := filepath.Join(t.TempDir(), "gh")
	script := "#!/bin/sh\n" +
		"case \"$GH_REPO:$GH_TOKEN\" in\n" +
		"  connextable/loki:token-456|jinyongp/personal:token-789) ;;\n" +
		"  *) exit 41 ;;\n" +
		"esac\n" +
		"printf '%s:%s:%s' \"$GH_REPO\" \"$1\" \"$2\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner := &CommandRunner{
		Config: CommandConfig{Binary: binary, CWD: t.TempDir(), Timeout: time.Second, MaxInputBytes: 4096, MaxOutputBytes: 4096},
		Tokens: broker,
	}
	for _, request := range []CommandRequest{
		{Target: "connextable/loki", Args: []string{"issue", "list"}},
		{Target: "connextable/loki", Args: []string{"pr", "list"}},
		{Target: "jinyongp/personal", Args: []string{"pr", "view"}},
		{Target: "jinyongp/personal", Args: []string{"api", "/repos/jinyongp/personal"}},
		{Target: "connextable/loki", Args: []string{"search", "issues", "is:open"}},
		{Target: "jinyongp/personal", Args: []string{"status"}},
	} {
		result, err := runner.Run(t.Context(), request)
		if err != nil || result.ExitCode != 0 || !strings.HasPrefix(result.Output, request.Target+":") {
			t.Fatalf("%#v: %#v %v", request, result, err)
		}
	}

	fields := &Client{
		Config: ClientConfig{APIVersion: "2026-03-10", Targets: []string{"connextable/loki"}, MaxResponseBytes: 4096, MaxPages: 2},
		HTTP:   server.Client(), Tokens: broker, apiURL: server.URL,
	}
	if values, err := fields.ListFields(t.Context(), "connextable/loki"); err != nil || len(values) != 0 {
		t.Fatal(values, err)
	}
	if _, err := fields.ListFields(t.Context(), "jinyongp/personal"); err == nil {
		t.Fatal("personal repository accepted for organization Issue Fields")
	}
	mu.Lock()
	defer mu.Unlock()
	if exchanges["token-456"] != 1 || exchanges["token-789"] != 1 {
		t.Fatalf("installation token exchanges: %#v", exchanges)
	}
}

func TestIntegrationRejectsMissingCredentialAndCommandEscapes(t *testing.T) {
	var tokenCalls int
	broker := &Broker{
		Config: BrokerConfig{
			AppID: 123, APIVersion: "2026-03-10", MaxResponseBytes: 4096,
			Targets: map[string]Target{"owner/repo": {InstallationID: 456, Repository: "repo"}},
		},
		Client: &http.Client{},
		PrivateKey: func(context.Context) (string, error) {
			tokenCalls++
			return LoadPrivateKeyFile(context.Background(), "/missing/github-app.pem")
		},
	}
	runner := &CommandRunner{
		Config: CommandConfig{Binary: "/usr/bin/false", CWD: t.TempDir(), Timeout: time.Second, MaxInputBytes: 4096, MaxOutputBytes: 4096},
		Tokens: broker,
	}
	for _, args := range [][]string{
		{"auth", "login"}, {"config", "set", "git_protocol", "ssh"},
		{"issue", "list", "--repo=other/repo"}, {"api", "--hostname", "attacker.invalid", "/user"},
	} {
		if _, err := runner.Run(t.Context(), CommandRequest{Target: "owner/repo", Args: args}); err == nil {
			t.Errorf("escape accepted: %#v", args)
		}
	}
	if tokenCalls != 0 {
		t.Fatalf("escape reached credential provider %d times", tokenCalls)
	}
	if _, err := runner.Run(t.Context(), CommandRequest{Target: "owner/repo", Args: []string{"issue", "list"}}); err == nil {
		t.Fatal("missing credential accepted")
	}
	if tokenCalls != 1 {
		t.Fatalf("credential provider calls=%d", tokenCalls)
	}
}
