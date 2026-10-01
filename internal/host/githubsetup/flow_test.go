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

func TestRunRelaysValidatedCallbackAndAppliesReadyInstallation(t *testing.T) {
	var redirect string
	var exchanges atomic.Int32
	var opened int
	var polls int
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
		if opened == 2 {
			if link != "https://github.com/apps/loki-test/installations/new" {
				t.Errorf("installation URL=%s", link)
			}
			return nil
		}
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
			response, err := http.Get(redirect + "?state=expected-state&code=one-time-code")
			if err != nil {
				return err
			}
			response.Body.Close()
			want := 200
			if i == 1 {
				want = 409
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
	if exchanges.Load() != 1 || opened != 2 || !strings.Contains(output.String(), "GitHub integration ready.") || !strings.Contains(output.String(), "example/repo") {
		t.Fatalf("exchanges=%d opened=%d output=%s", exchanges.Load(), opened, output.String())
	}
	if polls != 4 || strings.Count(output.String(), "Only select repositories") != 1 || strings.Count(output.String(), "Opening repository selection") != 1 {
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
