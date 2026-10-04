package githubapp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"loki/internal/config"
	"loki/internal/fault"
	githubsetup "loki/internal/integrations/github/setup"
	"loki/internal/state"
)

// Setup owns registration state and one-time conversion inside the protected
// GitHub volume. Its public views contain installation configuration only.
type Setup struct {
	Credentials   Credentials
	Configuration config.Config
	Client        *http.Client
	APIURL        string
}

type setupDocument struct {
	Schema        int       `json:"schema"`
	Phase         string    `json:"phase"`
	State         string    `json:"state,omitempty"`
	RedirectURL   string    `json:"redirect_url,omitempty"`
	Created       time.Time `json:"created,omitempty"`
	AppID         int64     `json:"app_id,omitempty"`
	Slug          string    `json:"slug,omitempty"`
	Personal      bool      `json:"personal,omitempty"`
	Configuration []byte    `json:"configuration,omitempty"`
}

func (s Setup) store() state.Store {
	return state.Store{Dir: filepath.Join(s.Credentials.StateDirectory, "setup"), Validate: func(raw json.RawMessage) error {
		var d setupDocument
		if json.Unmarshal(raw, &d) != nil || d.Schema != 1 {
			return fault.Error("invalid GitHub registration state")
		}
		switch d.Phase {
		case "":
			if d.AppID != 0 || d.State != "" || len(d.Configuration) != 0 {
				return fault.Error("invalid empty GitHub registration state")
			}
		case "registration", "exchange_pending":
			identity, err := hex.DecodeString(d.State)
			callback, parseErr := url.Parse(d.RedirectURL)
			if err != nil || len(identity) != 32 || d.Created.IsZero() || d.AppID != 0 || parseErr != nil || callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Path != "/callback" || callback.Port() == "" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" {
				return fault.Error("invalid GitHub callback state")
			}
		case "installation", "configured":
			if d.AppID <= 0 || !setupSlug(d.Slug) {
				return fault.Error("invalid GitHub registration App identity")
			}
			if d.Phase == "configured" {
				configuration, err := config.ParseGitHubFragment(d.Configuration)
				if err != nil || configuration.GitHubAppID != d.AppID || len(configuration.GitHubTargets) == 0 {
					return fault.Error("invalid GitHub installation state")
				}
			}
		default:
			return fault.Error("invalid GitHub registration phase")
		}
		return nil
	}}
}

// File-based recovery replaces a pending registration only after the private
// key was accepted. Its encrypted setup state contains no active key copy.
func (s Setup) Reset(ctx context.Context) error {
	store := s.store()
	if _, err := store.Initialize(ctx, json.RawMessage(`{"schema":1,"phase":""}`)); err != nil {
		return err
	}
	unlock, err := state.LockFile(ctx, filepath.Join(store.Dir, "registration.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return s.save(ctx, setupDocument{Schema: 1})
}

func (s Setup) save(ctx context.Context, d setupDocument) error {
	_, err := s.store().Update(ctx, nil, func(json.RawMessage) (json.RawMessage, error) { return json.Marshal(d) })
	return err
}

func (s Setup) api(ctx context.Context, method, endpoint, token string, result any) error {
	base := s.APIURL
	if base == "" {
		base = "https://api.github.com"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+endpoint, nil)
	if err != nil {
		return fault.Error("invalid GitHub registration request")
	}
	r.Header.Set("Accept", "application/vnd.github+json")
	r.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	r.Header.Set("User-Agent", "loki/0.2.1")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{}
	if s.Client != nil {
		client = *s.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(r)
	if err != nil {
		return fault.Error("GitHub setup request failed; check connectivity and retry setup")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fault.Error(fmt.Sprintf("GitHub setup request rejected (HTTP %d)", response.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	defer clear(raw)
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, result) != nil {
		return fault.Error("invalid bounded GitHub setup response")
	}
	return nil
}

func setupSlug(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, c := range value {
		if c < 'a' || c > 'z' {
			if c < '0' || c > '9' {
				if c != '-' {
					return false
				}
			}
		}
	}
	return true
}

func (s Setup) view(d setupDocument) githubsetup.View {
	v := githubsetup.View{SchemaVersion: 1, Phase: d.Phase, RequireConfirmation: true, Configuration: bytes.Clone(d.Configuration)}
	if d.Phase == "registration" {
		v.State = d.State
		v.RegistrationURL = "https://github.com/settings/apps/new?state=" + url.QueryEscape(d.State)
		v.Manifest = &githubsetup.Manifest{Name: "Loki-" + d.State[:12], URL: "https://github.com/jinyongp/loki", Public: true, RedirectURL: d.RedirectURL,
			HookAttributes: map[string]any{"url": "https://github.com/jinyongp/loki", "active": false}, DefaultEvents: []string{},
			DefaultPermissions: map[string]string{"metadata": "read", "contents": "write", "issues": "write", "pull_requests": "write", "actions": "write", "workflows": "write", "checks": "write", "statuses": "write", "organization_projects": "write", "issue_fields": "write", "issue_types": "write"}}
		if d.Personal {
			v.Manifest.DefaultPermissions["user_projects"] = "write"
		}
	}
	if d.AppID > 0 {
		v.InstallationURL = "https://github.com/apps/" + d.Slug + "/installations/new"
	}
	if len(d.Configuration) > 0 {
		if c, err := config.ParseGitHubFragment(d.Configuration); err == nil {
			accounts := []string{}
			for _, i := range c.GitHubInstallations {
				accounts = append(accounts, i.Account)
				v.Repositories = append(v.Repositories, i.Account+"/*")
			}
			v.Account = strings.Join(accounts, ", ")
		}
	}
	return v
}

func (s Setup) Handle(ctx context.Context, request githubsetup.Request) (githubsetup.View, error) {
	store := s.store()
	if _, err := store.Initialize(ctx, json.RawMessage(`{"schema":1,"phase":""}`)); err != nil {
		return githubsetup.View{}, err
	}
	unlock, err := state.LockFile(ctx, filepath.Join(store.Dir, "registration.lock"))
	if err != nil {
		return githubsetup.View{}, err
	}
	defer unlock()
	snapshot, err := store.Load(ctx)
	if err != nil {
		return githubsetup.View{}, err
	}
	defer clear(snapshot.Data)
	var d setupDocument
	if json.Unmarshal(snapshot.Data, &d) != nil {
		return githubsetup.View{}, fault.Error("invalid GitHub registration state")
	}
	switch request.Action {
	case "status":
		return s.view(d), nil
	case "begin":
		u, err := url.Parse(request.RedirectURL)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/callback" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return githubsetup.View{}, fault.Error("GitHub setup requires its loopback callback")
		}
		if d.Phase == "exchange_pending" {
			return githubsetup.View{}, fault.Error("GitHub App conversion outcome is uncertain; recover with file-based setup using the App's private key")
		}
		if s.Configuration.GitHubAppID > 0 && d.Phase == "" {
			key, err := s.Credentials.Get(ctx, AppPrivateKey)
			if err != nil {
				return githubsetup.View{}, err
			}
			jwt, err := AppJWT([]byte(key), s.Configuration.GitHubAppID, time.Now())
			if err != nil {
				return githubsetup.View{}, err
			}
			var app struct {
				ID   int64  `json:"id"`
				Slug string `json:"slug"`
			}
			if err := s.api(ctx, "GET", "/app", jwt, &app); err != nil {
				return githubsetup.View{}, err
			}
			if app.ID != s.Configuration.GitHubAppID || !setupSlug(app.Slug) {
				return githubsetup.View{}, fault.Error("GitHub App identity differs from its configured key")
			}
			d = setupDocument{Schema: 1, Phase: "installation", AppID: app.ID, Slug: app.Slug}
		} else if d.Phase == "" || d.Phase == "registration" && time.Since(d.Created) > time.Hour {
			var random [32]byte
			if _, err := rand.Read(random[:]); err != nil {
				return githubsetup.View{}, err
			}
			d = setupDocument{Schema: 1, Phase: "registration", State: hex.EncodeToString(random[:]), Created: time.Now()}
		}
		d.RedirectURL, d.Personal = request.RedirectURL, request.PersonalProjects
		if err := s.save(ctx, d); err != nil {
			return githubsetup.View{}, err
		}
	case "exchange":
		if d.Phase != "registration" || time.Since(d.Created) > time.Hour || subtle.ConstantTimeCompare([]byte(request.State), []byte(d.State)) != 1 || request.Code == "" || len(request.Code) > 256 || strings.ContainsAny(request.Code, "/?#\\\r\n") {
			return githubsetup.View{}, fault.Error("invalid or expired GitHub registration callback")
		}
		d.Phase = "exchange_pending"
		if err := s.save(ctx, d); err != nil {
			return githubsetup.View{}, err
		}
		var result struct {
			ID   int64  `json:"id"`
			Slug string `json:"slug"`
			PEM  string `json:"pem"`
		}
		if err := s.api(ctx, "POST", "/app-manifests/"+url.PathEscape(request.Code)+"/conversions", "", &result); err != nil {
			return githubsetup.View{}, err
		}
		if result.ID <= 0 || !setupSlug(result.Slug) || ValidatePrivateKey(result.PEM) != nil {
			return githubsetup.View{}, fault.Error("GitHub App conversion returned invalid credentials")
		}
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, err := s.Credentials.Set(persist, AppPrivateKey, result.PEM); err != nil {
			return githubsetup.View{}, err
		}
		result.PEM = ""
		d.AppID, d.Slug, d.Phase = result.ID, result.Slug, "installation"
		if err := s.save(persist, d); err != nil {
			return githubsetup.View{}, err
		}
	case "poll":
		if d.Phase != "installation" && d.Phase != "configured" {
			return githubsetup.View{}, fault.Error("GitHub setup has not reached installation")
		}
	case "finish":
		if d.Phase != "installation" && d.Phase != "configured" {
			return githubsetup.View{}, fault.Error("GitHub setup has not reached installation")
		}
		if err := s.discover(ctx, &d); err != nil {
			return githubsetup.View{}, err
		}
		if err := s.save(ctx, d); err != nil {
			return githubsetup.View{}, err
		}
	case "apply":
		if d.Phase != "configured" {
			return githubsetup.View{}, fault.Error("GitHub installation configuration is incomplete")
		}
		applied, err := config.ParseGitHubFragment(d.Configuration)
		if err != nil || s.Configuration.GitHubAppID != applied.GitHubAppID || len(s.Configuration.GitHubInstallations) != len(applied.GitHubInstallations) {
			return githubsetup.View{}, fault.Error("GitHub installation configuration has not been applied to this runtime")
		}
		for index, installation := range applied.GitHubInstallations {
			current := s.Configuration.GitHubInstallations[index]
			if current.InstallationID != installation.InstallationID || current.Account != installation.Account || current.AccountType != installation.AccountType || len(current.Repositories) != 1 || current.Repositories[0] != "*" {
				return githubsetup.View{}, fault.Error("GitHub installation access differs from the applied runtime")
			}
		}
		v := s.view(d)
		v.Phase = "ready"
		if err := s.save(ctx, setupDocument{Schema: 1}); err != nil {
			return githubsetup.View{}, err
		}
		return v, nil
	default:
		return githubsetup.View{}, fault.Error("unsupported GitHub setup action")
	}
	return s.view(d), nil
}

func (s Setup) discover(ctx context.Context, d *setupDocument) error {
	key, err := s.Credentials.Get(ctx, AppPrivateKey)
	if err != nil {
		return err
	}
	jwt, err := AppJWT([]byte(key), d.AppID, time.Now())
	if err != nil {
		return err
	}
	configuration := fmt.Sprintf("github_app_id = %d\ngithub_api_version = \"2026-03-10\"\n", d.AppID)
	seen := map[int64]bool{}
	for page := 1; page <= 16; page++ {
		var installations []struct {
			ID        int64      `json:"id"`
			AppID     int64      `json:"app_id"`
			Selection string     `json:"repository_selection"`
			Suspended *time.Time `json:"suspended_at"`
			Account   struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"account"`
		}
		if err := s.api(ctx, "GET", fmt.Sprintf("/app/installations?per_page=100&page=%d", page), jwt, &installations); err != nil {
			return err
		}
		for _, i := range installations {
			if i.Suspended != nil {
				continue
			}
			kind := "user"
			if i.Account.Type == "Organization" {
				kind = "organization"
			} else if i.Account.Type != "User" {
				return fault.Error("GitHub installation returned an unsupported account type")
			}
			if i.ID <= 0 || i.AppID != d.AppID || seen[i.ID] || i.Selection != "all" && i.Selection != "selected" {
				return fault.Error("GitHub installation identity is invalid")
			}
			seen[i.ID] = true
			configuration += fmt.Sprintf("\n[[github_installations]]\naccount = %s\naccount_type = %s\ninstallation_id = %d\nrepositories = [\"*\"]\n", strconv.Quote(strings.ToLower(i.Account.Login)), strconv.Quote(kind), i.ID)
		}
		if len(installations) < 100 {
			break
		}
		if page == 16 {
			return fault.Error("GitHub installation listing exceeds its complete-page bound")
		}
	}
	if len(seen) == 0 {
		return fault.Error("no approved GitHub App installations found; install the App, then retry setup")
	}
	parsed, err := config.ParseGitHubFragment([]byte(configuration))
	if err != nil || len(parsed.GitHubTargets) == 0 {
		return fault.Error("GitHub installations produced invalid repository configuration")
	}
	d.Configuration, d.Phase = []byte(configuration), "configured"
	return nil
}
