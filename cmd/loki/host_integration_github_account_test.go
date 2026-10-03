package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loki/internal/integrations/github/setup"
)

func TestGitHubBrowserDetectsAccountTypeAndResumesWithoutLookup(t *testing.T) {
	for _, kind := range []string{"User", "Organization"} {
		t.Run(kind, func(t *testing.T) {
			h, _ := browserSetupFixture(t, kind)
			request := githubsetup.Request{Action: "begin", Account: "example", RedirectURL: "http://127.0.0.1:42/callback"}
			view, err := h.Handle(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			want := "https://github.com/settings/apps/new?"
			if kind == "Organization" {
				want = "https://github.com/organizations/example/settings/apps/new?"
			}
			if !strings.HasPrefix(view.RegistrationURL, want) {
				t.Fatalf("registration=%s", view.RegistrationURL)
			}
			if view.Manifest == nil || !view.Manifest.Public {
				t.Fatal("new App must allow installations on additional personal and organization accounts")
			}
			if _, err = h.Handle(t.Context(), githubsetup.Request{Action: "exchange", State: view.State, Code: "code"}); err != nil {
				t.Fatal(err)
			}
			h.APIURL = "http://127.0.0.1:1"
			request.RedirectURL = "http://127.0.0.1:43/callback"
			resumed, err := h.Handle(t.Context(), request)
			if err != nil || resumed.Phase != "installation" {
				t.Fatalf("resume=%+v err=%v", resumed, err)
			}
		})
	}
}

func TestGitHubAccountDetectionRejectsInvalidOrUnavailableAccount(t *testing.T) {
	for _, body := range []string{`{"id":42,"login":"other","type":"User"}`, `{"id":42,"login":"example","type":"Bot"}`, `not-json`, "missing"} {
		t.Run(body, func(t *testing.T) {
			h, _ := browserSetupFixture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body == "missing" {
					http.NotFound(w, r)
					return
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			h.APIURL = server.URL
			_, err := h.Handle(t.Context(), githubsetup.Request{Action: "begin", Account: "example", RedirectURL: "http://127.0.0.1:42/callback"})
			if err == nil {
				t.Fatal("invalid account accepted")
			}
			raw, err := h.Store.ReadGitHubSetup(t.Context())
			if err != nil || len(raw) != 0 {
				t.Fatal("failed account lookup published setup state")
			}
		})
	}
}
