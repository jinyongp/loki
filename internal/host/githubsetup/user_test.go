package githubsetup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDeviceLoginRelayDisplaysOnlyPublicCode(t *testing.T) {
	var output bytes.Buffer
	const session = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var actions []string
	var opened string
	transport := func(_ context.Context, request UserRequest) (UserView, error) {
		actions = append(actions, request.Action)
		if request.Action == "begin" {
			if request.Account != "" || request.SessionID != "" {
				t.Fatal("invalid login begin")
			}
			return UserView{Status: "pending", Account: request.Account, SessionID: session, UserCode: "EXAM-PLE1", VerificationURI: "https://github.com/login/device", Interval: 1}, nil
		}
		if request.Account != "" || request.SessionID != session {
			t.Fatal("poll did not use opaque session")
		}
		return UserView{Status: "ready", Account: "example-user"}, nil
	}
	err := RunUser(t.Context(), transport, Options{OpenBrowser: func(raw string) error { opened = raw; return nil }}, &output)
	if err != nil || opened != "https://github.com/login/device" || strings.Join(actions, ",") != "begin,poll" {
		t.Fatal("device login failed", err, opened, actions)
	}
	if !strings.Contains(output.String(), "EXAM-PLE1") || !strings.Contains(output.String(), "authorization ready") || strings.Contains(output.String(), session) {
		t.Fatal("invalid public login output", output.String())
	}
}

func TestSetupIncludesPersonalProjectsAndSkipsUsableAuthorization(t *testing.T) {
	for _, scenario := range []string{"initial", "resume", "organization", "ready", "refreshable", "wrong-account", "disabled", "multiple"} {
		t.Run(scenario, func(t *testing.T) {
			var output bytes.Buffer
			applied := scenario != "initial"
			authorized := scenario == "ready" || scenario == "refreshable"
			second := false
			begins := 0
			setup := func(_ context.Context, request Request) (View, error) {
				if request.Action == "begin" && !applied {
					return View{Phase: "configured"}, nil
				}
				if request.Action == "apply" {
					applied = true
				}
				return View{Phase: "ready", Account: "example-user, example-org"}, nil
			}
			users := func(_ context.Context, request UserRequest) (UserView, error) {
				if !applied || request.Account != "" {
					t.Fatal("authorization preceded App installation or required a selector")
				}
				switch request.Action {
				case "status":
					if scenario == "organization" {
						return UserView{Status: "not_required"}, nil
					}
					status := "unconfigured"
					accountStatus := "unconfigured"
					if authorized {
						status, accountStatus = "ready", "ready"
					}
					if scenario == "refreshable" {
						accountStatus = "refresh_required"
					}
					view := UserView{Status: status, Accounts: []UserAccountView{{Account: "example-user", Status: accountStatus}}}
					if scenario == "multiple" {
						otherStatus := "unconfigured"
						if second {
							otherStatus = "ready"
						} else {
							view.Status = "unconfigured"
						}
						view.Accounts = append(view.Accounts, UserAccountView{Account: "another-user", Status: otherStatus})
					}
					return view, nil
				case "begin":
					begins++
					if scenario == "disabled" {
						return UserView{}, errors.New("enable Device flow in the GitHub App settings, then retry setup")
					}
					return UserView{Status: "pending", SessionID: strings.Repeat("a", 64), UserCode: "EXAM-PLE1", VerificationURI: "https://github.com/login/device", Interval: 1}, nil
				case "poll":
					if request.SessionID != strings.Repeat("a", 64) {
						t.Fatal("device code escaped the runtime")
					}
					if scenario == "wrong-account" {
						return UserView{Status: "ready", Account: "already-authorized-user"}, nil
					}
					account := "example-user"
					if authorized {
						second, account = true, "another-user"
					} else {
						authorized = true
					}
					return UserView{Status: "ready", Account: account}, nil
				default:
					t.Fatal("unexpected user action", request.Action)
					return UserView{}, nil
				}
			}
			err := Run(t.Context(), setup, Options{NoBrowser: true, UserTransport: users}, &output)
			if scenario == "wrong-account" || scenario == "disabled" {
				if err == nil || strings.Contains(output.String(), "GitHub integration ready") {
					t.Fatal("incomplete authorization reported as ready", err, output.String())
				}
				return
			}
			if err != nil || !strings.Contains(output.String(), "GitHub integration ready") {
				t.Fatal("unified setup failed", err, output.String())
			}
			want := 1
			if scenario == "organization" || scenario == "ready" || scenario == "refreshable" {
				want = 0
			}
			if scenario == "multiple" {
				want = 2
			}
			if begins != want {
				t.Fatal("setup reauthorized an account or omitted required authorization", begins, want)
			}
		})
	}
}

func TestDeviceLoginRejectsUntrustedURLAndCancelsWait(t *testing.T) {
	for _, scenario := range []string{"url", "account", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			view := UserView{Status: "pending", SessionID: strings.Repeat("a", 64), UserCode: "EXAM-PLE1", VerificationURI: "https://github.com/login/device", Interval: 5}
			if scenario == "url" {
				view.VerificationURI = "https://attacker.test/"
			}
			if scenario == "account" {
				view.Account = "another-user"
			}
			calls := 0
			transport := func(context.Context, UserRequest) (UserView, error) {
				calls++
				if scenario == "cancel" {
					cancel()
				}
				return view, nil
			}
			err := RunUser(ctx, transport, Options{NoBrowser: true}, &bytes.Buffer{})
			if err == nil || calls != 1 {
				t.Fatal("unsafe login response accepted or canceled login polled", err)
			}
		})
	}
}

func TestDeviceLoginRelayKeepsActionableErrorsAndDropsPrivateDetails(t *testing.T) {
	message := "enable Device flow in the GitHub App settings, then retry setup"
	if err := UserLoginError("loki: " + message + "\n"); err.Error() != message {
		t.Fatal("safe login advice lost", err)
	}
	for _, diagnostic := range []string{"GitHub user credentials are unavailable", "GitHub user credentials are invalid"} {
		if err := UserLoginError("loki: " + diagnostic); err.Error() != diagnostic {
			t.Fatal("credential diagnostic was replaced with Device flow advice", err)
		}
	}
	for _, detail := range []string{"ghu_private", "loki: authorize ghp_private", "docker failed with ghr_private", "loki: " + message + "\nghu_private"} {
		if err := UserLoginError(detail); strings.Contains(err.Error(), "private") {
			t.Fatal("relay exposed private diagnostic", err)
		}
	}
}

func TestUserStatusRejectsIncompleteOrContradictoryViews(t *testing.T) {
	request := UserRequest{Action: "status"}
	for _, view := range []UserView{
		{Status: "ready"},
		{Status: "not_required", Accounts: []UserAccountView{{Account: "example-user", Status: "ready"}}},
		{Status: "ready", Accounts: []UserAccountView{{Account: "example-user", Status: "expired"}}},
		{Status: "unconfigured", Accounts: []UserAccountView{{Account: "example-user", Status: "ready"}}},
		{Status: "unconfigured", Accounts: []UserAccountView{{Account: "example-user", Status: "unconfigured"}, {Account: "example-user", Status: "ready"}}},
	} {
		if err := view.Validate(request); err == nil {
			t.Fatal("incomplete authorization was accepted", view)
		}
	}
}
