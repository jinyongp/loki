package githubsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"loki/internal/progress"
)

type UserRequest struct {
	Action    string `json:"action"`
	Account   string `json:"account,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// UserView is the public device-login relay DTO. It contains no credentials
// and keeps the Windows frontend independent of Linux integration execution.
type UserView struct {
	Status          string            `json:"status"`
	Account         string            `json:"account,omitempty"`
	Accounts        []UserAccountView `json:"accounts,omitempty"`
	SessionID       string            `json:"session_id,omitempty"`
	UserCode        string            `json:"user_code,omitempty"`
	VerificationURI string            `json:"verification_uri,omitempty"`
	Interval        int               `json:"interval,omitempty"`
	ExpiresAt       time.Time         `json:"expires_at,omitzero"`
}

type UserAccountView struct {
	Account   string    `json:"account"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

type UserTransport func(context.Context, UserRequest) (UserView, error)

// Validate binds relay responses to their requested action. Aggregate status
// has account entries; a completed device authorization has one identified user.
func (v UserView) Validate(request UserRequest) error {
	invalid := errors.New("invalid GitHub user authorization response")
	switch request.Action {
	case "begin", "poll":
		if v.Status == "pending" && len(v.SessionID) == 64 && v.Interval >= 1 && v.Interval <= 900 {
			if request.Action == "begin" && (v.UserCode == "" || v.VerificationURI != "https://github.com/login/device" || v.Interval > 60 || v.Account != request.Account) {
				return invalid
			}
			if request.Action == "poll" && v.SessionID != request.SessionID {
				return invalid
			}
			return nil
		}
		if request.Action == "poll" && v.Status == "ready" && v.Account != "" {
			return nil
		}
	case "status", "logout":
		if request.Account != "" && v.Account == request.Account && (v.Status == "ready" || v.Status == "refresh_required" || v.Status == "expired" || v.Status == "unconfigured") {
			return nil
		}
		if request.Account != "" || v.Account != "" {
			return invalid
		}
		if v.Status == "not_required" && len(v.Accounts) == 0 {
			return nil
		}
		if (v.Status != "ready" && v.Status != "unconfigured") || len(v.Accounts) == 0 {
			return invalid
		}
		seen := map[string]bool{}
		ready := true
		for _, account := range v.Accounts {
			if account.Account == "" || seen[account.Account] {
				return invalid
			}
			seen[account.Account] = true
			switch account.Status {
			case "ready", "refresh_required":
			case "unconfigured", "expired":
				ready = false
			default:
				return invalid
			}
		}
		if ready == (v.Status == "ready") {
			return nil
		}
	}
	return invalid
}

// UserLoginError carries only known public runtime messages across CLI relays.
// Docker/WSL diagnostics and arbitrary upstream text may contain private data.
func UserLoginError(detail string) error {
	message := strings.TrimPrefix(progress.NonProgressText(detail), "loki: ")
	for _, allowed := range []string{
		"enable Device flow in the GitHub App settings, then retry setup",
		"authorize the configured personal account in GitHub, then retry setup",
		"GitHub personal Projects account is not allowed",
		"GitHub user authorization expired; retry setup",
		"GitHub user authorization was rejected; retry setup",
		"GitHub user authorization is not configured",
		"GitHub user credentials are unavailable",
		"GitHub user credentials are invalid",
		"GitHub App client ID is unavailable",
		"GitHub App credential is unavailable",
		"GitHub App authentication failed",
		"GitHub device authorization could not start; check the App's Device flow setting",
		"GitHub device authorization could not start",
		"GitHub authorization request failed",
		"GitHub authorization request was rejected",
		"GitHub authorization request timed out; retry setup",
		"GitHub authorization response is invalid",
		"GitHub user token response is invalid",
		"GitHub user credentials could not be saved",
		"GitHub user credentials could not be saved; retry setup",
	} {
		if message == allowed {
			return errors.New(message)
		}
	}
	return errors.New("GitHub user authorization failed; run integrations doctor github and retry setup")
}

// RunUser handles only the public device code and an opaque session handle.
// Device credentials and user tokens remain in the Linux runtime vault.
func RunUser(ctx context.Context, transport UserTransport, options Options, output io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 16*time.Minute)
	defer cancel()
	view, err := requestWithProgress(ctx, output, "Requesting a GitHub device code for personal Projects...", func() (UserView, error) {
		return transport(ctx, UserRequest{Action: "begin"})
	})
	if err != nil {
		return err
	}
	if view.Status != "pending" || view.Account != "" || len(view.SessionID) != 64 || view.UserCode == "" ||
		view.VerificationURI != "https://github.com/login/device" || view.Interval < 1 || view.Interval > 60 {
		return errors.New("invalid GitHub device login response")
	}
	fmt.Fprintln(output, "Authorize the GitHub App for personal Projects with your connected personal account.")
	fmt.Fprintln(output, "Code:", view.UserCode)
	fmt.Fprintln(output, "Open:", view.VerificationURI)
	if !options.NoBrowser {
		open := options.OpenBrowser
		if open == nil {
			open = OpenBrowser
		}
		_ = open(view.VerificationURI)
	}
	fmt.Fprintln(output, "Waiting for approval in GitHub; complete authorization in your browser.")
	stopHeartbeat := progress.StartHeartbeat(ctx, progress.NewLineReporter(output), progress.HeartbeatOptions{
		Operation: "github-setup", Phase: "user-authorization", Message: "Still waiting for personal Projects authorization in GitHub",
	})
	defer stopHeartbeat()
	session := view.SessionID
	for view.Status == "pending" {
		if view.Interval < 1 || view.Interval > 900 || view.SessionID != session || view.Account != "" {
			return errors.New("invalid GitHub device login response")
		}
		delay := time.Duration(view.Interval) * time.Second
		// Keep waits bounded and cancellable even after OAuth slow_down.
		for delay > 0 {
			step := min(delay, time.Minute)
			timer := time.NewTimer(step)
			select {
			case <-timer.C:
				delay -= step
			case <-ctx.Done():
				timer.Stop()
				return errors.New("GitHub device login stopped; retry setup")
			}
		}
		view, err = transport(ctx, UserRequest{Action: "poll", SessionID: session})
		if err != nil {
			return err
		}
	}
	if view.Status != "ready" || view.Account == "" {
		return errors.New("GitHub device login did not complete")
	}
	stopHeartbeat()
	fmt.Fprintln(output, "Personal Projects authorization ready for", view.Account)
	return nil
}

func finishSetup(ctx context.Context, options Options, output io.Writer, view View) error {
	if options.PersonalProjects && options.UserTransport == nil {
		return errors.New("personal Projects authorization is unavailable")
	}
	if options.PersonalProjects && options.UserTransport != nil {
		for {
			status, err := requestWithProgress(ctx, output, "Checking personal Projects authorization...", func() (UserView, error) {
				return options.UserTransport(ctx, UserRequest{Action: "status"})
			})
			if err != nil {
				return err
			}
			if err = status.Validate(UserRequest{Action: "status"}); err != nil {
				return err
			}
			if status.Status == "ready" || status.Status == "not_required" {
				break
			}
			if status.Status != "unconfigured" || len(status.Accounts) == 0 {
				return errors.New("invalid GitHub personal Projects status")
			}
			var pending []string
			for _, account := range status.Accounts {
				if account.Status != "ready" && account.Status != "refresh_required" {
					pending = append(pending, account.Account)
				}
			}
			if len(pending) == 0 {
				return errors.New("invalid GitHub personal Projects status")
			}
			fmt.Fprintln(output, "Personal Projects authorization required for:", strings.Join(pending, ", "))
			if err = RunUser(ctx, options.UserTransport, options, output); err != nil {
				if err.Error() == "enable Device flow in the GitHub App settings, then retry setup" {
					fmt.Fprintln(output, "Open GitHub App settings:", deviceFlowSettingsURL(view.AppSettingsURL))
					fmt.Fprintln(output, "Under Identifying and authorizing users, select Enable Device Flow and save, then rerun setup.")
				}
				return err
			}
			next, err := options.UserTransport(ctx, UserRequest{Action: "status"})
			if err != nil {
				return err
			}
			if err = next.Validate(UserRequest{Action: "status"}); err != nil {
				return err
			}
			progress := false
			for _, account := range next.Accounts {
				if account.Status == "ready" || account.Status == "refresh_required" {
					for _, name := range pending {
						if account.Account == name {
							progress = true
						}
					}
				}
			}
			if !progress {
				return errors.New("authorize a personal account listed above, then retry setup")
			}
		}
	}
	return printReady(output, view)
}

func deviceFlowSettingsURL(raw string) string {
	u, err := url.Parse(raw)
	if err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
		parts := strings.Split(strings.TrimSuffix(u.Path, "/advanced"), "/")
		if (len(parts) == 4 && parts[1] == "settings" && parts[2] == "apps" && parts[3] != "") ||
			(len(parts) == 6 && parts[1] == "organizations" && parts[2] != "" && parts[3] == "settings" && parts[4] == "apps" && parts[5] != "") {
			return "https://github.com" + strings.TrimSuffix(u.Path, "/advanced")
		}
	}
	return "https://github.com/settings/apps"
}
