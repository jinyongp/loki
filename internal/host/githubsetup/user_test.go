package githubsetup

import (
	"bytes"
	"context"
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
			if request.Account != "example-user" || request.SessionID != "" {
				t.Fatal("invalid login begin")
			}
			return UserView{Status: "pending", Account: request.Account, SessionID: session, UserCode: "EXAM-PLE1", VerificationURI: "https://github.com/login/device", Interval: 1}, nil
		}
		if request.Account != "" || request.SessionID != session {
			t.Fatal("poll did not use opaque session")
		}
		return UserView{Status: "ready", Account: "example-user"}, nil
	}
	err := RunUser(t.Context(), transport, "example-user", Options{OpenBrowser: func(raw string) error { opened = raw; return nil }}, &output)
	if err != nil || opened != "https://github.com/login/device" || strings.Join(actions, ",") != "begin,poll" {
		t.Fatal("device login failed", err, opened, actions)
	}
	if !strings.Contains(output.String(), "EXAM-PLE1") || !strings.Contains(output.String(), "authorization ready") || strings.Contains(output.String(), session) {
		t.Fatal("invalid public login output", output.String())
	}
}

func TestDeviceLoginRejectsUntrustedURLAndCancelsWait(t *testing.T) {
	for _, scenario := range []string{"url", "account", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			view := UserView{Status: "pending", Account: "example-user", SessionID: strings.Repeat("a", 64), UserCode: "EXAM-PLE1", VerificationURI: "https://github.com/login/device", Interval: 5}
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
			err := RunUser(ctx, transport, "example-user", Options{NoBrowser: true}, &bytes.Buffer{})
			if err == nil || calls != 1 {
				t.Fatal("unsafe login response accepted or canceled login polled", err)
			}
		})
	}
}

func TestDeviceLoginRelayKeepsActionableErrorsAndDropsPrivateDetails(t *testing.T) {
	message := "enable Device flow in the GitHub App settings, then retry login"
	if err := UserLoginError("loki: " + message + "\n"); err.Error() != message {
		t.Fatal("safe login advice lost", err)
	}
	for _, detail := range []string{"ghu_private", "loki: authorize ghp_private", "docker failed with ghr_private", "loki: " + message + "\nghu_private"} {
		if err := UserLoginError(detail); strings.Contains(err.Error(), "private") {
			t.Fatal("relay exposed private diagnostic", err)
		}
	}
}
