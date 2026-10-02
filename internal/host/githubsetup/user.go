package githubsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
)

type UserRequest struct {
	Action    string `json:"action"`
	Account   string `json:"account,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// UserView is the public device-login relay DTO. It contains no credentials
// and keeps the Windows frontend independent of Linux integration execution.
type UserView struct {
	Status          string    `json:"status"`
	Account         string    `json:"account"`
	SessionID       string    `json:"session_id,omitempty"`
	UserCode        string    `json:"user_code,omitempty"`
	VerificationURI string    `json:"verification_uri,omitempty"`
	Interval        int       `json:"interval,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitzero"`
}

type UserTransport func(context.Context, UserRequest) (UserView, error)

// UserLoginError carries only known public runtime messages across CLI relays.
// Docker/WSL diagnostics and arbitrary upstream text may contain private data.
func UserLoginError(detail string) error {
	message := strings.TrimPrefix(strings.TrimSpace(detail), "loki: ")
	for _, allowed := range []string{
		"enable Device flow in the GitHub App settings, then retry login",
		"authorize the configured personal account in GitHub, then retry login",
		"GitHub personal Projects account is not allowed",
		"GitHub user authorization expired; retry login",
		"GitHub user authorization was rejected; retry login",
		"GitHub user authorization is not configured",
		"GitHub App client ID is unavailable",
		"GitHub App credential is unavailable",
		"GitHub user credentials could not be saved; retry login",
	} {
		if message == allowed {
			return errors.New(message)
		}
	}
	return errors.New("GitHub user authorization failed; check Device flow and the configured personal account")
}

// RunUser handles only the public device code and an opaque session handle.
// Device credentials and user tokens remain in the Linux runtime vault.
func RunUser(ctx context.Context, transport UserTransport, account string, options Options, output io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 16*time.Minute)
	defer cancel()
	view, err := transport(ctx, UserRequest{Action: "begin", Account: account})
	if err != nil {
		return err
	}
	if view.Status != "pending" || view.Account != account || len(view.SessionID) != 64 || view.UserCode == "" ||
		view.VerificationURI != "https://github.com/login/device" || view.Interval < 1 || view.Interval > 60 {
		return errors.New("invalid GitHub device login response")
	}
	fmt.Fprintln(output, "Authorize the GitHub App for personal Projects as", account)
	fmt.Fprintln(output, "Code:", view.UserCode)
	fmt.Fprintln(output, "Open:", view.VerificationURI)
	if !options.NoBrowser {
		open := options.OpenBrowser
		if open == nil {
			open = OpenBrowser
		}
		_ = open(view.VerificationURI)
	}
	session := view.SessionID
	for view.Status == "pending" {
		if view.Interval < 1 || view.Interval > 900 || view.SessionID != session || view.Account != account {
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
				return errors.New("GitHub device login stopped; retry login")
			}
		}
		view, err = transport(ctx, UserRequest{Action: "poll", SessionID: session})
		if err != nil {
			return err
		}
	}
	if view.Status != "ready" || view.Account != account {
		return errors.New("GitHub device login did not complete")
	}
	fmt.Fprintln(output, "Personal Projects authorization ready for", account)
	return nil
}
