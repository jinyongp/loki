// Package githubsetup contains the browser relay shared by native Linux and Windows.
// GitHub credentials and durable setup state remain in the Linux host backend.
package githubsetup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Request struct {
	Action      string `json:"action"`
	RedirectURL string `json:"redirect_url,omitempty"`
	Account     string `json:"account,omitempty"`
	AccountType string `json:"account_type,omitempty"`
	State       string `json:"state,omitempty"`
	Code        string `json:"code,omitempty"`
}
type Manifest struct {
	Name                  string            `json:"name"`
	URL                   string            `json:"url"`
	Public                bool              `json:"public"`
	RedirectURL           string            `json:"redirect_url"`
	HookAttributes        map[string]any    `json:"hook_attributes"`
	DefaultPermissions    map[string]string `json:"default_permissions"`
	DefaultEvents         []string          `json:"default_events"`
	RequestOAuthOnInstall bool              `json:"request_oauth_on_install"`
}
type View struct {
	SchemaVersion   int       `json:"schema_version"`
	Phase           string    `json:"phase"`
	State           string    `json:"state,omitempty"`
	RegistrationURL string    `json:"registration_url,omitempty"`
	Manifest        *Manifest `json:"manifest,omitempty"`
	InstallationURL string    `json:"installation_url,omitempty"`
	Account         string    `json:"account,omitempty"`
	Repositories    []string  `json:"repositories,omitempty"`
	AppSettingsURL  string    `json:"app_settings_url,omitempty"`
}
type Transport func(context.Context, Request) (View, error)
type Options struct {
	Account, AccountType string
	NoBrowser            bool
	OpenBrowser          func(string) error
	PollInterval         time.Duration
	Timeout              time.Duration
}

// Run hosts only a loopback registration page and relays the one-time code.
// It never calls GitHub APIs or receives App private keys.
func Run(ctx context.Context, transport Transport, options Options, output io.Writer) error {
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt)
	defer stopSignals()
	timeout := options.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("cannot start GitHub setup callback")
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	view, err := transport(ctx, Request{Action: "begin", RedirectURL: origin + "/callback", Account: options.Account, AccountType: options.AccountType})
	if err != nil {
		return err
	}
	registration := view
	var lock sync.Mutex
	var submitted bool
	type callbackResult struct {
		view View
		err  error
	}
	completed := make(chan callbackResult, 1)
	installationRedirected := false
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		return err
	}
	localPath := "/start/" + hex.EncodeToString(tokenBytes)
	expectedHost := listener.Addr().String()
	page := template.Must(template.New("registration").Parse("<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>Connect GitHub to Loki</title><body><h1>Connect GitHub to Loki</h1><p>GitHub will ask you to create your App and choose repositories.</p><form method=\"post\" action=\"{{.URL}}\"><input type=\"hidden\" name=\"manifest\" value=\"{{.Manifest}}\"><button type=\"submit\">Create GitHub App</button></form></body></html>"))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action https://github.com; frame-ancestors 'none'")
		if r.Host != expectedHost || r.Method != http.MethodGet || len(r.URL.RawQuery) > 4096 {
			http.Error(w, "Invalid setup request", http.StatusBadRequest)
			return
		}
		if r.URL.Path == localPath && registration.Phase == "registration" {
			raw, encodeErr := json.Marshal(registration.Manifest)
			if encodeErr != nil {
				http.Error(w, "Cannot prepare registration", 500)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = page.Execute(w, struct{ URL, Manifest string }{registration.RegistrationURL, string(raw)})
			return
		}
		if r.URL.Path != "/callback" || registration.Phase != "registration" {
			http.NotFound(w, r)
			return
		}
		query, parseErr := url.ParseQuery(r.URL.RawQuery)
		if parseErr != nil || len(query["state"]) != 1 || len(query["code"]) != 1 || query.Get("state") != registration.State || query.Get("code") == "" {
			http.Error(w, "Invalid GitHub callback", http.StatusBadRequest)
			return
		}
		lock.Lock()
		defer lock.Unlock()
		if submitted {
			http.Error(w, "Callback already submitted", http.StatusConflict)
			return
		}
		next, submitErr := transport(ctx, Request{Action: "exchange", State: query.Get("state"), Code: query.Get("code")})
		if submitErr != nil {
			http.Error(w, "App setup could not complete. Return to the terminal.", http.StatusBadGateway)
			completed <- callbackResult{err: submitErr}
			submitted = true
			return
		}
		submitted = true
		if next.Phase == "installation" && next.InstallationURL != "" {
			http.Redirect(w, r, next.InstallationURL, http.StatusSeeOther)
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "App created. Return to the terminal to continue setup.")
		}
		completed <- callbackResult{view: next}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second, MaxHeaderBytes: 8192}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	open := options.OpenBrowser
	if open == nil {
		open = OpenBrowser
	}
	show := func(link string) {
		if options.NoBrowser {
			fmt.Fprintln(output, link)
			return
		}
		if openErr := open(link); openErr != nil {
			fmt.Fprintln(output, "Open this URL in a browser on this machine:", link)
		}
	}
	if view.Phase == "registration" {
		fmt.Fprintln(output, "Opening GitHub App registration...")
		show(origin + localPath)
		select {
		case result := <-completed:
			if result.err != nil {
				return result.err
			}
			view = result.view
			installationRedirected = view.Phase == "installation" && view.InstallationURL != ""
		case <-ctx.Done():
			return errors.New("GitHub setup stopped; rerun the same setup command to continue")
		}
	}
	if view.Phase == "ready" {
		return printReady(output, view)
	}
	if view.Phase == "installation" {
		if view.AppSettingsURL != "" {
			fmt.Fprintln(output, "Adding an account installation to the existing GitHub App. Existing accounts remain configured.")
			fmt.Fprintln(output, "If GitHub does not offer the requested account, check the App's installation visibility and the organization's App policy:", view.AppSettingsURL)
		}
		fmt.Fprintln(output, "Choose All repositories or Only select repositories, then click Install. Waiting for GitHub installation...")
		if !installationRedirected {
			show(view.InstallationURL)
		}
	}
	interval := options.PollInterval
	if interval == 0 {
		interval = 3 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		switch view.Phase {
		case "installation":
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return errors.New("GitHub installation is pending; rerun the same setup command to continue")
			}
			view, err = transport(ctx, Request{Action: "poll"})
		case "configured":
			fmt.Fprintln(output, "Verifying and applying GitHub integration...")
			view, err = transport(ctx, Request{Action: "apply"})
		case "ready":
			return printReady(output, view)
		default:
			return fmt.Errorf("unexpected GitHub setup phase %q", view.Phase)
		}
		if err != nil {
			return err
		}
	}
}
func printReady(output io.Writer, view View) error {
	fmt.Fprintln(output, "GitHub integration ready.")
	if view.Account != "" {
		fmt.Fprintln(output, "Account:", view.Account)
	}
	if len(view.Repositories) == 1 && strings.HasSuffix(view.Repositories[0], "/*") {
		fmt.Fprintln(output, "Repository access follows your GitHub App installation settings.")
	} else if len(view.Repositories) > 0 {
		fmt.Fprintln(output, "Repositories:", strings.Join(view.Repositories, ", "))
	}
	return nil
}
func OpenBrowser(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("invalid setup URL")
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", raw)
	case "darwin":
		command = exec.Command("open", raw)
	default:
		command = exec.Command("xdg-open", raw)
	}
	if err = command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
