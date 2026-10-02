package githubsetup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunAddsExistingAppInstallationWithoutRegistration(t *testing.T) {
	var opened []string
	var output bytes.Buffer
	transport := func(ctx context.Context, request Request) (View, error) {
		switch request.Action {
		case "begin":
			if request.Account != "" || request.AccountType != "" {
				t.Fatal("account selection belongs in GitHub")
			}
			return View{Phase: "installation", InstallationURL: "https://github.com/apps/existing-app/installations/new", AppSettingsURL: "https://github.com/settings/apps/existing-app/advanced"}, nil
		case "poll":
			return View{Phase: "configured"}, nil
		case "apply":
			return View{Phase: "ready", Account: "example-org", Repositories: []string{"example-org/*"}}, nil
		default:
			t.Fatalf("additional installation invoked registration: %s", request.Action)
			return View{}, errors.New("unexpected registration")
		}
	}
	if err := Run(t.Context(), transport, Options{Verbose: true, Input: strings.NewReader(""), PollInterval: time.Millisecond, OpenBrowser: func(link string) error { opened = append(opened, link); return nil }}, &output); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != "https://github.com/apps/existing-app/installations/new" || !strings.Contains(output.String(), "Existing accounts and local repository restrictions") || !strings.Contains(output.String(), "Make public") || !strings.Contains(output.String(), "https://github.com/settings/apps/existing-app/advanced") || !strings.Contains(output.String(), "GitHub integration ready") {
		t.Fatalf("opened=%v output=%s", opened, output.String())
	}
}

func TestRunFinishesExistingConfigureOnEnter(t *testing.T) {
	var output bytes.Buffer
	finished := false
	transport := func(ctx context.Context, request Request) (View, error) {
		switch request.Action {
		case "begin":
			if request.Account != "" || request.AccountType != "" {
				t.Fatal("browser flow should choose account in GitHub")
			}
			return View{Phase: "installation", InstallationURL: "https://github.com/apps/existing-app/installations/new", AppSettingsURL: "https://github.com/settings/apps/existing-app/advanced"}, nil
		case "finish":
			finished = true
			return View{Phase: "ready", Account: "example-org"}, nil
		default:
			t.Fatalf("existing Configure should not register or apply: %s", request.Action)
			return View{}, errors.New("unexpected action")
		}
	}
	err := Run(t.Context(), transport, Options{Input: strings.NewReader("\n"), PollInterval: time.Hour, OpenBrowser: func(string) error { return nil }}, &output)
	if err != nil || !finished || !strings.Contains(output.String(), "press Enter") {
		t.Fatalf("finish=%t output=%s err=%v", finished, output.String(), err)
	}
}

func TestRunWaitsForAllAccountsBeforeApplyingBatch(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "existing"}[existing], func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			var output bytes.Buffer
			var finishes, applies atomic.Int32
			opened := make(chan struct{})
			transport := func(_ context.Context, request Request) (View, error) {
				switch request.Action {
				case "begin":
					view := View{Phase: "installation", InstallationURL: "https://github.com/apps/existing-app/installations/new", RequireConfirmation: true}
					if existing {
						view.AppSettingsURL = "https://github.com/settings/apps/existing-app/advanced"
					}
					return view, nil
				case "finish":
					finishes.Add(1)
					return View{Phase: "configured"}, nil
				case "apply":
					applies.Add(1)
					return View{Phase: "ready", Account: "example-user, example-org", Repositories: []string{"example-user/*", "example-org/*"}}, nil
				default:
					t.Errorf("batch must wait for Enter, received %s", request.Action)
					return View{}, errors.New("unexpected action")
				}
			}
			result := make(chan error, 1)
			go func() {
				result <- Run(t.Context(), transport, Options{Input: reader, PollInterval: time.Millisecond, OpenBrowser: func(string) error { close(opened); return nil }}, &output)
			}()
			<-opened
			select {
			case err := <-result:
				t.Fatalf("setup ended before Enter: %v", err)
			case <-time.After(15 * time.Millisecond):
			}
			if finishes.Load() != 0 || applies.Load() != 0 {
				t.Fatal("batch applied before browser selection was complete")
			}
			if _, err := io.WriteString(writer, "\n"); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if finishes.Load() != 1 || applies.Load() != 1 || !strings.Contains(output.String(), "Accounts: example-user, example-org") || !strings.Contains(output.String(), "Repository access follows") || strings.Contains(output.String(), "paste its GitHub Configure") {
				t.Fatalf("batch output=%s", output.String())
			}
			if strings.Count(output.String(), "\n") > 6 || strings.Contains(output.String(), "Make public") || strings.Contains(output.String(), "organization App policies") {
				t.Fatalf("default setup output includes detailed instructions: %s", output.String())
			}
		})
	}
}

func TestRunConnectsPastedGitHubConfigureURL(t *testing.T) {
	var output bytes.Buffer
	selected := false
	transport := func(ctx context.Context, request Request) (View, error) {
		switch request.Action {
		case "begin":
			return View{Phase: "installation", InstallationURL: "https://github.com/apps/existing-app/installations/new", AppSettingsURL: "https://github.com/settings/apps/existing-app/advanced"}, nil
		case "select":
			selected = true
			if request.InstallationID != 789 {
				t.Fatalf("installation=%d", request.InstallationID)
			}
			return View{Phase: "configured"}, nil
		case "apply":
			return View{Phase: "ready", Account: "example-org"}, nil
		default:
			t.Fatalf("unexpected selection action %s", request.Action)
			return View{}, errors.New("unexpected action")
		}
	}
	input := "https://evil.example/settings/installations/789\nhttps://github.com/organizations/example-org/settings/installations/789\n"
	err := Run(t.Context(), transport, Options{Input: strings.NewReader(input), PollInterval: time.Hour, OpenBrowser: func(string) error { return nil }}, &output)
	if err != nil || !selected {
		t.Fatalf("selection=%t err=%v", selected, err)
	}
}

func TestConfigureInstallationIDRejectsOtherURLs(t *testing.T) {
	for _, raw := range []string{"http://github.com/settings/installations/1", "https://github.com.evil.example/settings/installations/1", "https://user@github.com/settings/installations/1", "https://github.com/settings/apps/example", "https://github.com/settings/installations/-1", "https://github.com/settings/installations/999999999999999999999", "https://github.com/organizations/example-org/settings/installations/1/other"} {
		if _, valid := configureInstallationID(raw); valid {
			t.Fatalf("accepted invalid installation URL: %s", raw)
		}
	}
	for _, raw := range []string{"https://github.com/settings/installations/789", "https://github.com/organizations/example-org/settings/installations/789"} {
		if id, valid := configureInstallationID(raw); !valid || id != 789 {
			t.Fatalf("rejected valid installation URL: %s", raw)
		}
	}
}

func TestRunRelaysValidatedCallbackAndAppliesReadyInstallation(t *testing.T) {
	var redirect string
	var exchanges atomic.Int32
	var opened int
	var polls int
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	transport := func(ctx context.Context, req Request) (View, error) {
		switch req.Action {
		case "begin":
			redirect = req.RedirectURL
			return View{SchemaVersion: 1, Phase: "registration", State: "expected-state", RegistrationURL: "https://github.com/settings/apps/new?state=expected-state", Manifest: &Manifest{Name: "Loki-test", RedirectURL: req.RedirectURL}}, nil
		case "exchange":
			exchanges.Add(1)
			if req.State != "expected-state" || req.Code != "one-time-code" {
				t.Error("invalid callback relayed")
			}
			return View{Phase: "installation", InstallationURL: "https://github.com/apps/loki-test/installations/new"}, nil
		case "poll":
			polls++
			if polls < 4 {
				return View{Phase: "installation"}, nil
			}
			return View{Phase: "configured"}, nil
		case "apply":
			return View{Phase: "ready", Account: "example", Repositories: []string{"example/repo"}}, nil
		default:
			return View{}, errors.New("unexpected request")
		}
	}
	open := func(link string) error {
		opened++
		res, err := http.Get(link)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(body), "Create GitHub App") || !strings.Contains(string(body), "https://github.com/settings/apps/new") {
			t.Errorf("registration page: %d %s", res.StatusCode, body)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Error("page can be cached")
		}
		for _, query := range []string{"state=wrong&code=one-time-code", "state=expected-state&code=a&code=b", "state=expected-state&code=%zz"} {
			response, err := http.Get(redirect + "?" + query)
			if err != nil {
				return err
			}
			response.Body.Close()
			if response.StatusCode != 400 {
				t.Errorf("invalid callback status=%d", response.StatusCode)
			}
		}
		req, _ := http.NewRequest("GET", link, nil)
		req.Host = "attacker.example"
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Error("foreign host accepted")
		}
		for i := 0; i < 2; i++ {
			response, err := client.Get(redirect + "?state=expected-state&code=one-time-code")
			if err != nil {
				return err
			}
			response.Body.Close()
			want := http.StatusSeeOther
			if i == 1 {
				want = 409
			} else if response.Header.Get("Location") != "https://github.com/apps/loki-test/installations/new" {
				t.Errorf("callback redirect=%s", response.Header.Get("Location"))
			}
			if response.StatusCode != want {
				t.Errorf("callback status=%d want=%d", response.StatusCode, want)
			}
		}
		return nil
	}
	var output bytes.Buffer
	if err := Run(t.Context(), transport, Options{OpenBrowser: open, PollInterval: time.Millisecond}, &output); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 1 || opened != 1 || !strings.Contains(output.String(), "GitHub integration ready.") || !strings.Contains(output.String(), "example/repo") {
		t.Fatalf("exchanges=%d opened=%d output=%s", exchanges.Load(), opened, output.String())
	}
	if polls != 4 || strings.Count(output.String(), "All repositories or Only select repositories") != 1 {
		t.Fatalf("polls=%d output=%s", polls, output.String())
	}
}

func TestRunResumesWithoutRegistrationAndPreservesApplyFailure(t *testing.T) {
	var begins, applies int
	transport := func(ctx context.Context, req Request) (View, error) {
		if req.Action == "begin" {
			begins++
			return View{Phase: "configured"}, nil
		}
		if req.Action == "apply" {
			applies++
			return View{}, errors.New("repository access validation failed")
		}
		t.Errorf("unexpected action %s", req.Action)
		return View{}, errors.New("unexpected")
	}
	var output bytes.Buffer
	err := Run(t.Context(), transport, Options{OpenBrowser: func(string) error { t.Error("resume opened registration"); return nil }}, &output)
	if err == nil || !strings.Contains(err.Error(), "repository access") || begins != 1 || applies != 1 || strings.Contains(output.String(), "integration ready") {
		t.Fatalf("err=%v output=%s", err, output.String())
	}
}

func TestRunStopsPendingInstallationWithoutClaimingReady(t *testing.T) {
	var output bytes.Buffer
	err := Run(t.Context(), func(context.Context, Request) (View, error) {
		return View{Phase: "installation", InstallationURL: "https://github.com/apps/example/installations/new"}, nil
	}, Options{NoBrowser: true, Timeout: 5 * time.Millisecond, PollInterval: time.Hour}, &output)
	if err == nil || !strings.Contains(err.Error(), "pending") || strings.Contains(output.String(), "integration ready") {
		t.Fatalf("err=%v output=%s", err, output.String())
	}
}

func TestCallbackFailureReturnsHostRecoveryMessage(t *testing.T) {
	var redirect string
	transport := func(_ context.Context, r Request) (View, error) {
		if r.Action == "begin" {
			redirect = r.RedirectURL
			return View{Phase: "registration", State: "state", RegistrationURL: "https://github.com/settings/apps/new", Manifest: &Manifest{}}, nil
		}
		return View{}, errors.New("conversion outcome uncertain; use file-based setup")
	}
	err := Run(t.Context(), transport, Options{OpenBrowser: func(string) error {
		response, err := http.Get(redirect + "?" + url.Values{"state": {"state"}, "code": {"code"}}.Encode())
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if strings.Contains(string(body), "conversion outcome") {
			t.Error("backend failure leaked to callback page")
		}
		return nil
	}}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("err=%v", err)
	}
}
