package compose

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"loki/internal/host/githubsetup"
)

// GitHubUserAuthorization forwards a closed host-administration command into
// the runtime. No device code, private key, or user token crosses this relay.
func (b *Backend) GitHubUserAuthorization(ctx context.Context, request githubsetup.UserRequest) (githubsetup.UserView, error) {
	value := request.Account
	switch request.Action {
	case "begin", "status", "logout":
		if value == "" || len(value) > 39 || strings.HasPrefix(value, "-") || request.SessionID != "" {
			return githubsetup.UserView{}, errors.New("invalid GitHub personal account")
		}
	case "poll":
		if request.Account != "" || len(request.SessionID) != 64 {
			return githubsetup.UserView{}, errors.New("invalid GitHub authorization session")
		}
		value = request.SessionID
	default:
		return githubsetup.UserView{}, errors.New("invalid GitHub user authorization action")
	}
	state, found, err := b.loadRuntime()
	if err != nil || !found {
		return githubsetup.UserView{}, errors.New("Loki runtime is unavailable")
	}
	raw, err := b.compose(ctx, state, "exec", "-T", "runtime", "/opt/loki/bin/loki", "github", "user", request.Action,
		"--runtime-socket", "/run/loki/runtime/control.sock", "--", value)
	if err != nil {
		return githubsetup.UserView{}, githubsetup.UserLoginError(string(raw))
	}
	var view githubsetup.UserView
	if json.Unmarshal(raw, &view) != nil || view.Status == "" || view.Account == "" {
		return view, errors.New("invalid GitHub user authorization response")
	}
	return view, nil
}
