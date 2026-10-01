package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"loki/internal/config"
	"loki/internal/host/githubsetup"
)

func TestGitHubBrowserFollowsInstallationSelectionWithoutRepositorySnapshot(t *testing.T) {
	for _, selection := range []string{"selected", "all"} {
		t.Run(selection, func(t *testing.T) {
			h, _ := browserSetupFixture(t, "User", selection)
			begin, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", RedirectURL: "http://127.0.0.1:42/callback"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: begin.State, Code: "code"}); err != nil {
				t.Fatal(err)
			}
			transport := h.Client.Transport
			h.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/app/installations" {
					t.Fatalf("discovery must not snapshot repository access: %s", r.URL.Path)
				}
				return transport.RoundTrip(r)
			})
			view, err := h.Handle(t.Context(), githubsetup.Request{Action: "poll"})
			if err != nil || view.Phase != "configured" || len(view.Repositories) != 1 || view.Repositories[0] != "example/*" {
				t.Fatalf("view=%+v err=%v", view, err)
			}
			raw, err := h.Store.ReadGitHubSetup(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer clear(raw)
			var session githubSetupSession
			if err = json.Unmarshal(raw, &session); err != nil {
				t.Fatal(err)
			}
			defer clear(session.PrivateKey)
			parsed, err := config.ParseGitHubFragment(session.ConfigRaw)
			if err != nil || parsed.GitHubTargets[0] != "example/*" {
				t.Fatalf("config=%+v err=%v", parsed, err)
			}
		})
	}
}

func TestManagedGitHubAllRepositoriesValidatesInstallationAndRevokesProbeToken(t *testing.T) {
	for _, scenario := range []string{"repository", "mixed-case", "empty", "other-owner"} {
		t.Run(scenario, func(t *testing.T) {
			candidate := managedGitHubCandidateFixture(t)
			defer clear(candidate.KeyRaw)
			candidate.ConfigRaw = []byte(strings.Replace(string(candidate.ConfigRaw), `["repo"]`, `["*"]`, 1))
			var err error
			candidate.Config, err = config.ParseGitHubFragment(candidate.ConfigRaw)
			if err != nil {
				t.Fatal(err)
			}
			var revoked bool
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body := ""
				status := 200
				switch r.URL.Path {
				case "/app/installations/456":
					owner := "example-org"
					if scenario == "other-owner" {
						owner = "other"
					}
					body = fmt.Sprintf(`{"id":456,"app_id":123,"account":{"login":%q,"type":"Organization"}}`, owner)
				case "/app/installations/456/access_tokens":
					var input map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					status = 201
					if _, probe := input["permissions"]; probe {
						body = `{"token":"probe-token"}`
					} else {
						if string(input["repositories"]) != `["repo"]` {
							t.Fatalf("command token is not repository scoped: %v", input)
						}
						body = `{"token":"repo-token","expires_at":"2099-01-01T00:00:00Z"}`
					}
				case "/installation/repositories":
					if r.Header.Get("Authorization") != "Bearer probe-token" {
						t.Fatal("invalid probe credential")
					}
					body = `{"total_count":1,"repositories":[{"name":"repo","owner":{"login":"example-org"}}]}`
					if scenario == "mixed-case" {
						body = `{"total_count":1,"repositories":[{"name":"RePo","owner":{"login":"Example-ORG"}}]}`
					}
					if scenario == "empty" {
						body = `{"total_count":0,"repositories":[]}`
					}
				case "/installation/token":
					revoked = true
					status = 204
				case "/repos/example-org/repo":
					body = `{"full_name":"example-org/repo","default_branch":"main","html_url":"https://github.com/example-org/repo"}`
					if scenario == "mixed-case" {
						body = `{"full_name":"Example-ORG/RePo","default_branch":"main","html_url":"https://github.com/Example-ORG/RePo"}`
					}
				default:
					t.Fatalf("unexpected request %s", r.URL.Path)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			err = validateManagedGitHubCandidate(t.Context(), candidate, client)
			if scenario == "other-owner" {
				if err == nil {
					t.Fatal("foreign installation accepted")
				}
				return
			}
			if err != nil || !revoked {
				t.Fatalf("err=%v revoked=%v", err, revoked)
			}
		})
	}
}
