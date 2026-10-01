package githubapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestAllRepositoryBrokerIssuesOneRepositoryTokenForNewTargets(t *testing.T) {
	broker, requests, _ := brokerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Repositories) != 1 || (body.Repositories[0] != "new-repo" && body.Repositories[0] != "second") || r.URL.Path != "/app/installations/456/access_tokens" {
			t.Fatalf("token request=%+v path=%s err=%v", body, r.URL.Path, err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"token":"scoped-token","expires_at":"2026-09-15T01:00:00Z"}`)
	})
	broker.Config.Targets = map[string]Target{"example-org/*": {InstallationID: 456, Repository: "*"}}
	for _, target := range []string{"example-org/new-repo", "example-org/second"} {
		if _, err := broker.Token(t.Context(), target); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{"other/new-repo", "example-org/*", "example-org/../other", "example-org/new/repo"} {
		if _, err := broker.Token(t.Context(), target); err == nil {
			t.Fatalf("invalid target accepted: %s", target)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestAllRepositorySelectionMatchesProviderAndIssueFieldsTargets(t *testing.T) {
	provider := &Provider{Config: ProviderConfig{APIVersion: "2026-03-10", Targets: []string{"owner/*"}, MaxResponseBytes: 4096, MaxPages: 1}, HTTP: &http.Client{}, Tokens: &Broker{}}
	client := &Client{Config: ClientConfig{Targets: []string{"owner/*"}}}
	for _, target := range []string{"owner/new-repo", "owner/second", "other/repo", "owner/*", "owner/../repo", "owner/a/b"} {
		want := target == "owner/new-repo" || target == "owner/second"
		_, _, _, providerErr := provider.target(target)
		_, _, clientErr := client.target(target)
		if (providerErr == nil) != want || (clientErr == nil) != want {
			t.Fatalf("target=%s provider=%v issue-fields=%v", target, providerErr, clientErr)
		}
	}
}

func TestInstallationRepositoryAccessChangesWithoutReconfiguration(t *testing.T) {
	allowed := false
	broker, requests, _ := brokerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Repositories) != 1 || body.Repositories[0] != "new-repo" {
			t.Fatalf("request=%+v err=%v", body, err)
		}
		if !allowed {
			http.Error(w, "repository not installed", http.StatusUnprocessableEntity)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"token":"scoped-token","expires_at":"2026-09-15T01:00:00Z"}`)
	})
	broker.Config.Targets = map[string]Target{"example-org/*": {InstallationID: 456, Repository: "*"}}
	if _, err := broker.Token(t.Context(), "example-org/new-repo"); err == nil {
		t.Fatal("GitHub access denial was ignored")
	}
	allowed = true
	if _, err := broker.Token(t.Context(), "example-org/new-repo"); err != nil {
		t.Fatalf("new GitHub selection requires no local refresh: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests=%d", requests.Load())
	}
}
