package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUserDeviceAuthorizationPersistsRefreshesAndLogsOut(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	private := testKey(t, 2048)
	var stored string
	var polls, refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				t.Error("App discovery did not use its JWT")
			}
			ioJSON(w, map[string]any{"id": 123, "client_id": "Iv1.example"})
		case "/login/device/code":
			_ = r.ParseForm()
			if r.Form.Get("client_id") != "Iv1.example" || r.Header.Get("Authorization") != "" {
				t.Error("invalid device authorization request")
			}
			ioJSON(w, map[string]any{"device_code": "private-device-code", "user_code": "EXAM-PLE1", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5})
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") == "refresh_token" {
				refreshes.Add(1)
				if r.Form.Get("refresh_token") != "ghr_first" || r.Form.Get("client_secret") != "" || r.Form.Get("client_id") != "Iv1.example" {
					t.Error("refresh did not use the device-flow credentials")
				}
				ioJSON(w, userTokenResponse{AccessToken: "ghu_rotated", RefreshToken: "ghr_rotated", TokenType: "bearer", ExpiresIn: 28800, RefreshTokenExpiresIn: 15897600})
				return
			}
			if r.Form.Get("device_code") != "private-device-code" {
				t.Error("device code not retained privately")
			}
			switch polls.Add(1) {
			case 1:
				ioJSON(w, userTokenResponse{Error: "authorization_pending"})
			case 2:
				ioJSON(w, userTokenResponse{Error: "slow_down", Interval: 10})
			default:
				ioJSON(w, userTokenResponse{AccessToken: "ghu_first", RefreshToken: "ghr_first", TokenType: "bearer", ExpiresIn: 28800, RefreshTokenExpiresIn: 15897600})
			}
		case "/user":
			if r.Header.Get("Authorization") != "Bearer ghu_first" && r.Header.Get("Authorization") != "Bearer ghu_rotated" {
				t.Error("user identity lookup used the wrong credential")
			}
			ioJSON(w, map[string]any{"id": 42, "login": "Example-User", "type": "User"})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	u := &UserAuthorization{AppID: 123, Accounts: []string{"another-user", "example-user"}, HTTP: server.Client(),
		PrivateKey: func(context.Context) (string, error) { return private, nil },
		Load:       func(context.Context) (string, error) { return stored, nil },
		Save:       func(_ context.Context, value string) error { stored = value; return nil },
		Now:        func() time.Time { return now }, apiURL: server.URL, loginURL: server.URL}
	view, err := u.Begin(t.Context(), "")
	if err != nil || view.Status != "pending" || view.Account != "" || len(view.SessionID) != 64 || view.UserCode != "EXAM-PLE1" {
		t.Fatal("device authorization did not start", view, err)
	}
	assertUserViewHasNoSecrets(t, view)
	if _, err = u.Poll(t.Context(), view.SessionID); err != nil || polls.Load() != 0 {
		t.Fatal("polled before the GitHub interval", err)
	}
	for index, seconds := range []int{5, 5, 10} {
		now = now.Add(time.Duration(seconds) * time.Second)
		next, err := u.Poll(t.Context(), view.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if index == 1 && next.Interval != 10 || index == 2 && next.Status != "ready" {
			t.Fatal("incorrect device flow state", next)
		}
		if index == 2 && next.Account != "example-user" {
			t.Fatal("signed-in account was not identified", next)
		}
		assertUserViewHasNoSecrets(t, next)
	}
	if token, err := u.AccountToken(t.Context(), "example-user"); err != nil || token != "ghu_first" {
		t.Fatal("saved user credential unavailable", err)
	}
	if token, err := u.AccountToken(t.Context(), "another-user"); err == nil || token != "" {
		t.Fatal("authorization attached to a different installed account")
	}
	if _, err := u.Poll(t.Context(), view.SessionID); err == nil {
		t.Fatal("completed device session replayed")
	}
	// A runtime restart must load the encrypted credential, not require a new
	// device login. Concurrent calls refresh the consumed token exactly once.
	now = now.Add(8 * time.Hour)
	u = &UserAuthorization{AppID: u.AppID, Accounts: u.Accounts, HTTP: u.HTTP, PrivateKey: u.PrivateKey,
		Load: u.Load, Save: u.Save, Now: u.Now, apiURL: server.URL, loginURL: server.URL}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if token, err := u.AccountToken(t.Context(), "example-user"); err != nil || token != "ghu_rotated" {
				t.Error("rotated credential unavailable", err)
			}
		})
	}
	workers.Wait()
	if refreshes.Load() != 1 || strings.Contains(stored, "ghu_first") || strings.Contains(stored, "ghr_first") {
		t.Fatal("refresh rotation was not persisted exactly once")
	}
	status, err := u.Status(t.Context(), "example-user")
	if err != nil || status.Status != "ready" {
		t.Fatal("status did not report saved authorization", err)
	}
	assertUserViewHasNoSecrets(t, status)
	if _, err := u.Logout(t.Context(), "example-user"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.AccountToken(t.Context(), "example-user"); err == nil || strings.Contains(stored, "ghu_") || strings.Contains(stored, "ghr_") {
		t.Fatal("logout retained active credentials")
	}
}

func TestUserAuthorizationAggregateStatusAndLogout(t *testing.T) {
	now := time.Now().UTC()
	credentials := map[string]userCredential{
		"example-user": {AppID: 123, Account: "example-user", ClientID: "Iv1.example", AccessToken: "ghu_example", RefreshToken: "ghr_example", ExpiresAt: now.Add(-time.Minute), RefreshExpires: now.Add(time.Hour)},
		"another-user": {AppID: 999, Account: "another-user", AccessToken: "ghu_stale"},
		"removed-user": {AppID: 123, Account: "removed-user", AccessToken: "ghu_removed"},
	}
	raw, _ := json.Marshal(credentials)
	stored := string(raw)
	u := &UserAuthorization{AppID: 123, Accounts: []string{"example-user", "another-user"}, HTTP: http.DefaultClient,
		PrivateKey: func(context.Context) (string, error) {
			t.Fatal("local status used network credentials")
			return "", nil
		},
		Load: func(context.Context) (string, error) { return stored, nil }, Save: func(_ context.Context, value string) error { stored = value; return nil }, Now: func() time.Time { return now },
		pending: map[string]*userDeviceSession{"opaque-session": {deadline: now.Add(time.Minute)}},
	}
	status, err := u.Status(t.Context(), "")
	if err != nil || status.Status != "unconfigured" || len(status.Accounts) != 2 || status.Accounts[0].Status != "refresh_required" || status.Accounts[1].Status != "unconfigured" {
		t.Fatal("aggregate status skipped missing or stale App authorization", status, err)
	}
	assertUserViewHasNoSecrets(t, status)
	if _, err = u.Logout(t.Context(), ""); err != nil || stored != "{}" || len(u.pending) != 0 {
		t.Fatal("logout retained local authorization", err)
	}
	u.Accounts = nil
	if status, err := u.Status(t.Context(), ""); err != nil || status.Status != "not_required" {
		t.Fatal("organization setup requested user authorization", status, err)
	}
	if _, err := u.Begin(t.Context(), ""); err == nil {
		t.Fatal("organization-only setup began personal authorization")
	}
}

func ioJSON(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }

func assertUserViewHasNoSecrets(t *testing.T, view UserAuthorizationView) {
	t.Helper()
	raw, _ := json.Marshal(view)
	for _, secret := range []string{"private-device-code", "ghu_", "ghr_", "access_token", "refresh_token", "device_code", "PRIVATE KEY"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("authorization view exposes credential material")
		}
	}
}

func TestUserLoginRejectsAnotherAccountAndMissingDeviceFlow(t *testing.T) {
	private := testKey(t, 2048)
	for _, scenario := range []string{"wrong-user", "disabled", "denied", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC()
			var saves int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/app":
					ioJSON(w, map[string]any{"id": 123, "client_id": "Iv1.example"})
				case "/login/device/code":
					if scenario == "disabled" {
						ioJSON(w, map[string]any{"error": "device_flow_disabled"})
					} else {
						ioJSON(w, map[string]any{"device_code": "private-device-code", "user_code": "EXAM-PLE1", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5})
					}
				case "/login/oauth/access_token":
					if scenario == "denied" {
						ioJSON(w, map[string]any{"error": "access_denied", "error_description": "private-device-code ghu_should-not-leak"})
					} else {
						ioJSON(w, userTokenResponse{AccessToken: "ghu_example", TokenType: "bearer"})
					}
				case "/user":
					ioJSON(w, map[string]any{"id": 43, "login": "another-user", "type": "User"})
				}
			}))
			defer server.Close()
			u := &UserAuthorization{AppID: 123, Accounts: []string{"example-user"}, HTTP: server.Client(),
				PrivateKey: func(context.Context) (string, error) { return private, nil }, Load: func(context.Context) (string, error) { return "", nil },
				Save: func(context.Context, string) error { saves++; return nil }, Now: func() time.Time { return now }, apiURL: server.URL, loginURL: server.URL}
			if _, err := u.Begin(t.Context(), "another-user"); err == nil {
				t.Fatal("unconfigured personal account accepted")
			}
			view, err := u.Begin(t.Context(), "")
			if scenario != "disabled" {
				if err != nil {
					t.Fatal(err)
				}
				now = now.Add(5 * time.Second)
				if scenario == "expired" {
					now = now.Add(15 * time.Minute)
				}
				_, err = u.Poll(t.Context(), view.SessionID)
			}
			if err == nil || saves != 0 || strings.Contains(err.Error(), "private-device-code") || strings.Contains(err.Error(), "ghu_") {
				t.Fatal("failed authorization saved or leaked credentials", err)
			}
		})
	}
}

func TestUserAuthorizationRejectsUnsafeResponsesAndStoreFailures(t *testing.T) {
	for _, response := range []userTokenResponse{
		{AccessToken: "ghp_pat", TokenType: "bearer"},
		{AccessToken: "ghu_example", TokenType: "basic"},
		{AccessToken: "ghu_example", TokenType: "bearer", Scope: "repo"},
		{AccessToken: "ghu_example", TokenType: "bearer", ExpiresIn: 28800},
		{AccessToken: "ghu_example", TokenType: "bearer", RefreshToken: "ghr_example"},
		{AccessToken: "ghu_bad\nvalue", TokenType: "bearer"},
	} {
		if _, err := response.credential(123, "example-user", "Iv1.example", time.Now()); err == nil {
			t.Fatal("unsafe user token accepted")
		}
	}
	var redirects atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1); ioJSON(w, map[string]any{}) }))
	defer destination.Close()
	for _, scenario := range []string{"redirect", "large", "trailing", "rejected"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch scenario {
			case "redirect":
				http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
			case "large":
				_, _ = w.Write([]byte(strings.Repeat("x", 65537)))
			case "trailing":
				_, _ = w.Write([]byte(`{} {}`))
			case "rejected":
				w.WriteHeader(403)
				_, _ = w.Write([]byte("ghu_example"))
			}
		}))
		var out any
		err := authorizationJSON(t.Context(), server.Client(), http.MethodGet, server.URL, "ghu_example", nil, &out)
		server.Close()
		if err == nil || strings.Contains(err.Error(), "ghu_example") {
			t.Fatal("unsafe response accepted", scenario, err)
		}
	}
	if redirects.Load() != 0 {
		t.Fatal("authorization followed a redirect")
	}
	u := &UserAuthorization{Load: func(context.Context) (string, error) { return "", errors.New("ghu_private") }}
	if _, err := u.credentials(t.Context()); err == nil || strings.Contains(err.Error(), "ghu_private") {
		t.Fatal("store failure leaked credential", err)
	}
	u.Save = func(context.Context, string) error { return errors.New("ghu_private") }
	if err := u.save(t.Context(), map[string]userCredential{}); err == nil || strings.Contains(err.Error(), "ghu_private") {
		t.Fatal("save failure leaked credential", err)
	}
}

func TestUserTokensRejectChangedAppExpiredRefreshAndRevocation(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"changed-app", "expired-refresh", "revoked-refresh", "malformed-store"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				ioJSON(w, map[string]any{"error": "bad_refresh_token", "error_description": "ghr_private"})
			}))
			defer server.Close()
			credential := userCredential{AppID: 123, ClientID: "Iv1.example", Account: "example-user", AccessToken: "ghu_private", RefreshToken: "ghr_private", ExpiresAt: now.Add(-time.Minute), RefreshExpires: now.Add(time.Hour)}
			if scenario == "changed-app" {
				credential.AppID = 456
			}
			if scenario == "expired-refresh" {
				credential.RefreshExpires = now.Add(-time.Minute)
			}
			raw, _ := json.Marshal(map[string]userCredential{"example-user": credential})
			if scenario == "malformed-store" {
				raw = []byte(`{} {}`)
			}
			var saves int
			u := &UserAuthorization{AppID: 123, Accounts: []string{"example-user"}, HTTP: server.Client(),
				PrivateKey: func(context.Context) (string, error) { return "unused", nil },
				Load:       func(context.Context) (string, error) { return string(raw), nil },
				Save:       func(context.Context, string) error { saves++; return nil },
				Now:        func() time.Time { return now }, loginURL: server.URL}
			_, err := u.AccountToken(t.Context(), "example-user")
			if err == nil || saves != 0 || strings.Contains(err.Error(), "ghu_private") || strings.Contains(err.Error(), "ghr_private") {
				t.Fatal("invalid user credential used or disclosed", err)
			}
			wantRequests := int32(0)
			if scenario == "revoked-refresh" {
				wantRequests = 1
			}
			if requests.Load() != wantRequests {
				t.Fatal("invalid stored credential reached OAuth endpoint")
			}
		})
	}
}
