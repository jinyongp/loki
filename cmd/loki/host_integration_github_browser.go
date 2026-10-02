package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"loki/internal/config"
	"loki/internal/host/githubsetup"
	"loki/internal/host/lifecycle"
	githubapp "loki/internal/integrations/github"
)

type githubSetupSession struct {
	Version              int                          `json:"version"`
	Phase                string                       `json:"phase"`
	State                string                       `json:"state"`
	CreatedAt            time.Time                    `json:"created_at"`
	RedirectURL          string                       `json:"redirect_url"`
	Account              string                       `json:"account,omitempty"`
	AccountType          string                       `json:"account_type"`
	AppID                int64                        `json:"app_id,omitempty"`
	Slug                 string                       `json:"slug,omitempty"`
	OwnerID              int64                        `json:"owner_id,omitempty"`
	PrivateKey           []byte                       `json:"private_key,omitempty"`
	ConfigRaw            []byte                       `json:"config,omitempty"`
	Repositories         []string                     `json:"repositories,omitempty"`
	BaseConfig           []byte                       `json:"base_config,omitempty"`
	AppSettingsURL       string                       `json:"app_settings_url,omitempty"`
	SelectInstallation   bool                         `json:"select_installation,omitempty"`
	InstallationBaseline []githubInstallationSnapshot `json:"installation_baseline,omitempty"`
}
type hostGitHubSetup struct {
	Store     *lifecycle.FileStore
	Client    *http.Client
	APIURL    string
	Now       func() time.Time
	Reconcile func(context.Context) error
	Apply     func(context.Context, managedGitHubCandidate) error
	Ready     func(context.Context) (bool, error)
}

func (h *hostGitHubSetup) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}
func (h *hostGitHubSetup) Handle(ctx context.Context, request githubsetup.Request) (githubsetup.View, error) {
	switch request.Action {
	case "begin", "exchange", "poll", "finish", "select", "apply":
	default:
		return githubsetup.View{}, errors.New("invalid GitHub setup request")
	}
	if request.Action == "select" && request.InstallationID <= 0 {
		return githubsetup.View{}, errors.New("GitHub installation ID is invalid")
	}
	if request.Action == "begin" {
		if err := validateGitHubSetupBegin(request); err != nil {
			return githubsetup.View{}, err
		}
	}
	lock, err := h.Store.GitHubSetupLock(ctx)
	if err != nil {
		return githubsetup.View{}, err
	}
	defer lock.Close()
	if h.Reconcile != nil {
		if err = h.Reconcile(ctx); err != nil {
			return githubsetup.View{}, err
		}
	}
	current, err := h.Store.ReadManagedIntegrations(ctx)
	if err != nil {
		return githubsetup.View{}, err
	}
	raw, err := h.Store.ReadGitHubSetup(ctx)
	if err != nil {
		return githubsetup.View{}, err
	}
	defer clear(raw)
	var session githubSetupSession
	if len(raw) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&session); err != nil || !session.valid() {
			return githubsetup.View{}, errors.New("GitHub setup state is invalid")
		}
		var trailing any
		if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return githubsetup.View{}, errors.New("GitHub setup state has trailing content")
		}
	}
	defer func() { clear(session.PrivateKey) }()
	if current.GitHub.Configured {
		view, configuredErr := h.configuredSetup(ctx, request, current, &session)
		if configuredErr != nil {
			return githubsetup.View{}, configuredErr
		}
		if view != nil {
			return *view, nil
		}
	}
	if request.Action == "begin" {
		if request.AccountType == "" {
			if session.Phase != "" && (request.Account == "" || strings.EqualFold(request.Account, session.Account)) {
				request.AccountType = session.AccountType
			} else if request.Account != "" {
				request.AccountType, err = h.accountType(ctx, request.Account)
				if err != nil {
					return githubsetup.View{}, err
				}
			} else {
				request.AccountType = "user"
			}
		}
		if session.Phase == "exchange_pending" {
			return githubsetup.View{}, errors.New("GitHub App creation result is uncertain. Check your GitHub App settings, generate a private key, and use file-based setup to recover")
		}
		if session.Phase != "" && session.Phase != "registration" {
			accountType := request.AccountType
			if accountType == "" {
				accountType = "user"
			}
			if request.Account != "" && (!strings.EqualFold(request.Account, session.Account) || accountType != session.AccountType) {
				return githubsetup.View{}, errors.New("a GitHub setup for another account is pending; finish that setup first")
			}
			return session.view(), nil
		}
		if session.Phase == "registration" {
			accountType := request.AccountType
			if accountType == "" {
				accountType = "user"
			}
			if !strings.EqualFold(request.Account, session.Account) || accountType != session.AccountType {
				return githubsetup.View{}, errors.New("a GitHub registration for another account is pending")
			}
			if h.now().Sub(session.CreatedAt) <= time.Hour {
				// Retain the App name and callback state when restarting registration.
				// A concurrent callback is serialized and cannot replay conversion.
				session.RedirectURL = request.RedirectURL
				if err = h.save(ctx, session); err != nil {
					return githubsetup.View{}, err
				}
				return session.view(), nil
			}
		}
		state := make([]byte, 32)
		if _, err = rand.Read(state); err != nil {
			return githubsetup.View{}, err
		}
		session = githubSetupSession{Version: 1, Phase: "registration", State: hex.EncodeToString(state), CreatedAt: h.now(), RedirectURL: request.RedirectURL, Account: strings.ToLower(request.Account), AccountType: request.AccountType, SelectInstallation: request.Account == ""}
		if session.AccountType == "" {
			session.AccountType = "user"
		}
		if err = h.save(ctx, session); err != nil {
			return githubsetup.View{}, err
		}
		return session.view(), nil
	}
	if session.Phase == "" {
		return githubsetup.View{}, errors.New("GitHub setup has not started")
	}
	switch request.Action {
	case "exchange":
		if subtle.ConstantTimeCompare([]byte(request.State), []byte(session.State)) != 1 || !validSetupCode(request.Code) {
			return githubsetup.View{}, errors.New("invalid GitHub registration callback")
		}
		if session.AppID > 0 {
			return session.view(), nil
		}
		if session.Phase != "registration" {
			return githubsetup.View{}, errors.New("GitHub App conversion outcome is uncertain; use file-based setup to recover")
		}
		if h.now().Sub(session.CreatedAt) > time.Hour {
			return githubsetup.View{}, errors.New("GitHub registration expired; rerun setup")
		}
		// Conversion is not replay-safe. Persist intent before the outbound request.
		session.Phase = "exchange_pending"
		if err = h.save(ctx, session); err != nil {
			return githubsetup.View{}, err
		}
		var result struct {
			ID    int64  `json:"id"`
			Slug  string `json:"slug"`
			PEM   string `json:"pem"`
			Owner struct {
				ID    int64  `json:"id"`
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"owner"`
		}
		if err = h.api(ctx, http.MethodPost, "/app-manifests/"+request.Code+"/conversions", "", nil, &result); err != nil {
			return githubsetup.View{}, err
		}
		if result.ID <= 0 || result.Owner.ID <= 0 || !setupName(result.Slug, 100) || !setupName(result.Owner.Login, 39) || githubapp.ValidatePrivateKey(result.PEM) != nil {
			return githubsetup.View{}, errors.New("GitHub App conversion returned invalid App metadata")
		}
		accountType := "user"
		if result.Owner.Type == "Organization" {
			accountType = "organization"
		} else if result.Owner.Type != "User" {
			return githubsetup.View{}, errors.New("GitHub App owner type is invalid")
		}
		if accountType != session.AccountType || (session.Account != "" && !strings.EqualFold(session.Account, result.Owner.Login)) {
			return githubsetup.View{}, errors.New("GitHub App was created for a different account")
		}
		session.AppID = result.ID
		session.Slug = result.Slug
		session.OwnerID = result.Owner.ID
		session.Account = strings.ToLower(result.Owner.Login)
		session.PrivateKey = []byte(result.PEM)
		result.PEM = ""
		session.Phase = "installation"
		// A successful conversion cannot be replayed. Publish its known key even
		// if the caller stopped while receiving the response.
		if err = h.save(context.WithoutCancel(ctx), session); err != nil {
			return githubsetup.View{}, err
		}
	case "poll", "finish", "select":
		if session.Phase == "configured" {
			return session.view(), nil
		}
		if session.Phase != "installation" {
			return githubsetup.View{}, errors.New("GitHub App is not ready for installation")
		}
		if request.Action == "select" {
			err = h.discoverSelectedInstallation(ctx, &session, request.InstallationID)
		} else {
			err = h.discover(ctx, &session)
		}
		if err != nil {
			return githubsetup.View{}, err
		}
		if len(session.BaseConfig) > 0 && (bytes.Equal(session.ConfigRaw, session.BaseConfig) || request.Action == "finish" && session.Phase == "installation") {
			parsed, parseErr := config.ParseGitHubFragment(session.BaseConfig)
			if parseErr != nil {
				return githubsetup.View{}, parseErr
			}
			view, readyErr := h.currentGitHubReady(ctx, parsed, true)
			if readyErr != nil {
				return githubsetup.View{}, readyErr
			}
			return *view, nil
		}
		if err = h.save(ctx, session); err != nil {
			return githubsetup.View{}, err
		}
	case "apply":
		if session.Phase != "configured" || h.Apply == nil {
			return githubsetup.View{}, errors.New("GitHub installation is not ready to apply")
		}
		parsed, parseErr := config.ParseGitHubFragment(session.ConfigRaw)
		if parseErr != nil {
			return githubsetup.View{}, errors.New("GitHub setup configuration is invalid")
		}
		if err = h.Apply(ctx, managedGitHubCandidate{ConfigRaw: session.ConfigRaw, KeyRaw: session.PrivateKey, Config: parsed}); err != nil {
			return githubsetup.View{}, err
		}
		if h.Ready == nil {
			return githubsetup.View{}, errors.New("GitHub readiness check is unavailable")
		}
		ready, checkErr := h.Ready(ctx)
		if checkErr != nil {
			return githubsetup.View{}, checkErr
		}
		if !ready {
			return githubsetup.View{}, errors.New("GitHub configuration was applied but is not ready; run integration doctor github")
		}
		session.Phase = "ready"
		if err = h.Store.ClearGitHubSetup(ctx); err != nil {
			return githubsetup.View{}, err
		}
		if session.SelectInstallation {
			view := session.view()
			accounts := make([]string, 0, len(parsed.GitHubInstallations))
			for _, installation := range parsed.GitHubInstallations {
				accounts = append(accounts, installation.Account)
			}
			view.Account, view.Repositories = strings.Join(accounts, ", "), append([]string(nil), parsed.GitHubTargets...)
			return view, nil
		}
	}
	return session.view(), nil
}
func (s githubSetupSession) valid() bool {
	state, err := hex.DecodeString(s.State)
	if err != nil || len(state) != 32 || s.Version != 1 || s.CreatedAt.IsZero() {
		return false
	}
	if validateGitHubSetupBegin(githubsetup.Request{RedirectURL: s.RedirectURL, Account: s.Account, AccountType: s.AccountType}) != nil {
		return false
	}
	switch s.Phase {
	case "registration", "exchange_pending":
		return s.AppID == 0 && len(s.PrivateKey) == 0 && len(s.ConfigRaw) == 0
	case "installation", "configured":
		if s.AppID <= 0 || s.OwnerID <= 0 || !setupName(s.Slug, 100) || !setupName(s.Account, 39) || githubapp.ValidatePrivateKey(string(s.PrivateKey)) != nil {
			return false
		}
		if len(s.BaseConfig) > 0 {
			base, err := config.ParseGitHubFragment(s.BaseConfig)
			if err != nil || base.GitHubAppID != s.AppID || len(base.GitHubInstallations) == 0 {
				return false
			}
		}
		return s.Phase != "configured" || len(s.ConfigRaw) > 0
	default:
		return false
	}
}
func (h *hostGitHubSetup) save(ctx context.Context, session githubSetupSession) error {
	raw, err := json.Marshal(session)
	if err != nil {
		return errors.New("cannot encode GitHub setup state")
	}
	defer clear(raw)
	return h.Store.WriteGitHubSetup(ctx, raw)
}
func (s githubSetupSession) view() githubsetup.View {
	view := githubsetup.View{SchemaVersion: 1, Phase: s.Phase, Account: s.Account, Repositories: append([]string(nil), s.Repositories...), AppSettingsURL: s.AppSettingsURL}
	if s.Phase == "registration" {
		target := "https://github.com/settings/apps/new"
		if s.AccountType == "organization" {
			target = "https://github.com/organizations/" + s.Account + "/settings/apps/new"
		}
		view.State = s.State
		view.RegistrationURL = target + "?state=" + url.QueryEscape(s.State)
		view.Manifest = &githubsetup.Manifest{
			Name: "Loki-" + s.State[:12], URL: "https://github.com/jinyongp/loki", Public: true,
			RedirectURL: s.RedirectURL, HookAttributes: map[string]any{"url": "https://github.com/jinyongp/loki", "active": false},
			DefaultPermissions: map[string]string{"metadata": "read", "contents": "write", "issues": "write", "pull_requests": "write"},
			DefaultEvents:      []string{}, RequestOAuthOnInstall: false,
		}
	}
	if s.AppID > 0 {
		view.InstallationURL = "https://github.com/apps/" + s.Slug + "/installations/new"
	}
	return view
}
func validateGitHubSetupBegin(request githubsetup.Request) error {
	u, err := url.Parse(request.RedirectURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/callback" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("GitHub setup callback must be a loopback URL")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("GitHub setup callback port is invalid")
	}
	if request.AccountType != "" && request.AccountType != "user" && request.AccountType != "organization" {
		return errors.New("GitHub account type must be user or organization")
	}
	if request.Account != "" && !setupName(request.Account, 39) {
		return errors.New("GitHub account is invalid")
	}
	if request.AccountType == "organization" && request.Account == "" {
		return errors.New("organization setup requires --account")
	}
	return nil
}

func (h *hostGitHubSetup) accountType(ctx context.Context, account string) (string, error) {
	_, kind, err := h.accountIdentity(ctx, account)
	return kind, err
}

func (h *hostGitHubSetup) accountIdentity(ctx context.Context, account string) (int64, string, error) {
	var result struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	if err := h.api(ctx, http.MethodGet, "/users/"+url.PathEscape(account), "", nil, &result); err != nil {
		return 0, "", fmt.Errorf("cannot detect GitHub account type; verify --account or provide --account-type: %w", err)
	}
	if result.ID <= 0 || !strings.EqualFold(result.Login, account) {
		return 0, "", errors.New("GitHub account lookup returned invalid metadata")
	}
	switch result.Type {
	case "Organization":
		return result.ID, "organization", nil
	case "User":
		return result.ID, "user", nil
	default:
		return 0, "", errors.New("GitHub account is not a user or organization")
	}
}
func setupName(value string, max int) bool {
	if value == "" || len(value) > max || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") || strings.Contains(value, "..") {
		return false
	}
	return setupRepositoryName(value)
}
func setupRepositoryName(value string) bool {
	if value == "" || len(value) > 100 || strings.Contains(value, "..") {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-", r)) {
			return false
		}
	}
	return true
}
func validSetupCode(value string) bool {
	if len(value) < 1 || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (h *hostGitHubSetup) api(ctx context.Context, method, path, token string, body any, result any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return errors.New("cannot encode GitHub setup request")
		}
	}
	defer clear(payload)
	base := h.APIURL
	if base == "" {
		base = "https://api.github.com"
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return errors.New("cannot build GitHub setup request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "loki")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{}
	if h.Client != nil {
		client = *h.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return errors.New("GitHub setup request failed; check connectivity and retry setup")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("GitHub setup request rejected (HTTP %d)", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	defer clear(raw)
	if err != nil || len(raw) > 1<<20 {
		return errors.New("GitHub setup response exceeds the supported size")
	}
	if result == nil {
		return nil
	}
	if err = json.Unmarshal(raw, result); err != nil {
		return errors.New("GitHub setup returned invalid JSON")
	}
	return nil
}
func (h *hostGitHubSetup) discover(ctx context.Context, s *githubSetupSession) error {
	if s.SelectInstallation {
		return h.discoverSelectedInstallation(ctx, s, 0)
	}
	jwt, err := githubapp.AppJWT(s.PrivateKey, s.AppID, h.now())
	if err != nil {
		return err
	}
	type installation struct {
		ID      int64 `json:"id"`
		AppID   int64 `json:"app_id"`
		Account struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
		Selection   string     `json:"repository_selection"`
		SuspendedAt *time.Time `json:"suspended_at"`
	}
	var chosen *installation
	for page := 1; page <= 16; page++ {
		var items []installation
		if err = h.api(ctx, "GET", fmt.Sprintf("/app/installations?per_page=100&page=%d", page), jwt, nil, &items); err != nil {
			return err
		}
		for i := range items {
			item := items[i]
			if item.AppID == s.AppID && item.Account.ID == s.OwnerID && strings.EqualFold(item.Account.Login, s.Account) {
				if chosen != nil {
					return errors.New("multiple matching GitHub installations returned")
				}
				chosen = &item
			}
		}
		if len(items) < 100 {
			break
		}
		if page == 16 {
			return errors.New("GitHub installation listing is incomplete")
		}
	}
	if chosen == nil {
		return nil
	}
	if chosen.ID <= 0 {
		return errors.New("GitHub installation ID is invalid")
	}
	if chosen.SuspendedAt != nil {
		return errors.New("GitHub App installation is suspended")
	}
	if chosen.Selection != "selected" && chosen.Selection != "all" {
		return errors.New("GitHub installation returned an invalid repository selection")
	}
	// GitHub checks the installation's current selection when issuing each
	// repository-scoped token. Keep the installation binding, not a snapshot.
	if chosen.Account.Type != "" && ((s.AccountType == "user" && chosen.Account.Type != "User") || (s.AccountType == "organization" && chosen.Account.Type != "Organization")) {
		return errors.New("GitHub installation account type does not match setup")
	}
	base := s.BaseConfig
	if len(base) == 0 {
		base = []byte(fmt.Sprintf("github_app_id = %d\ngithub_api_version = \"2026-03-10\"\n", s.AppID))
	}
	s.ConfigRaw = append(append([]byte(nil), base...), []byte(fmt.Sprintf("\n[[github_installations]]\naccount = %s\naccount_type = %s\ninstallation_id = %d\nrepositories = [\"*\"]\n", strconv.Quote(s.Account), strconv.Quote(s.AccountType), chosen.ID))...)
	if _, err = config.ParseGitHubFragment(s.ConfigRaw); err != nil {
		return errors.New("GitHub setup generated invalid configuration")
	}
	s.Repositories = []string{s.Account + "/*"}
	s.Phase = "configured"
	return nil
}
