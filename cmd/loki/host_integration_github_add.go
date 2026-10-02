package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"loki/internal/config"
	"loki/internal/host/githubsetup"
	"loki/internal/host/lifecycle"
	githubapp "loki/internal/integrations/github"
)

// configuredSetup resumes an additive installation session or returns the
// verified current configuration. An addition never replaces existing accounts.
func (h *hostGitHubSetup) configuredSetup(ctx context.Context, request githubsetup.Request, current lifecycle.ManagedIntegrationState, session *githubSetupSession) (*githubsetup.View, error) {
	raw, parsed, err := loadManagedGitHubPublicConfig(ctx, h.Store)
	if err != nil {
		return nil, err
	}
	if !current.GitHub.Enabled {
		return nil, errors.New("GitHub is configured but disabled; run integration enable github")
	}
	if len(session.BaseConfig) > 0 {
		if request.Action == "begin" && request.Account != "" && (!strings.EqualFold(request.Account, session.Account) || request.AccountType != "" && request.AccountType != session.AccountType) {
			return nil, errors.New("another GitHub account installation is pending; finish that setup first")
		}
		if parsed.GitHubAppID != session.AppID || current.GitHub.CredentialSHA256 != lifecycle.ManagedIntegrationDigest(session.PrivateKey) {
			return nil, errors.New("GitHub App credentials changed during setup; the current integration was preserved")
		}
		if session.Phase == "configured" && bytes.Equal(raw, session.ConfigRaw) {
			return h.currentGitHubReady(ctx, parsed, true)
		}
		if !bytes.Equal(raw, session.BaseConfig) {
			return nil, errors.New("GitHub configuration changed during setup; the current integration was preserved")
		}
		return nil, nil
	}
	for _, installation := range parsed.GitHubInstallations {
		if strings.EqualFold(request.Account, installation.Account) {
			if request.AccountType != "" && request.AccountType != installation.AccountType {
				return nil, errors.New("requested account type does not match the configured GitHub installation")
			}
			return h.currentGitHubReady(ctx, parsed, true)
		}
	}
	if request.Account == "" {
		if request.Action != "begin" {
			return h.currentGitHubReady(ctx, parsed, true)
		}
	}
	if request.Action != "begin" {
		return nil, errors.New("GitHub account installation setup has not started")
	}
	if session.Phase != "" && (session.AppID != parsed.GitHubAppID || lifecycle.ManagedIntegrationDigest(session.PrivateKey) != current.GitHub.CredentialSHA256) {
		return nil, errors.New("another GitHub App setup is pending; finish that setup first")
	}
	if _, err = h.currentGitHubReady(ctx, parsed, false); err != nil {
		return nil, err
	}
	addition, err := h.beginGitHubInstallationAddition(ctx, request, raw, current)
	if err != nil {
		return nil, err
	}
	*session = addition
	view := session.view()
	return &view, nil
}

func (h *hostGitHubSetup) currentGitHubReady(ctx context.Context, parsed config.Config, clearSession bool) (*githubsetup.View, error) {
	if h.Ready == nil {
		return nil, errors.New("GitHub readiness check is unavailable")
	}
	ready, err := h.Ready(ctx)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, errors.New("GitHub is configured but not ready; run integration doctor github")
	}
	if clearSession {
		if err = h.Store.ClearGitHubSetup(ctx); err != nil {
			return nil, err
		}
	}
	accounts := make([]string, 0, len(parsed.GitHubInstallations))
	for _, installation := range parsed.GitHubInstallations {
		accounts = append(accounts, installation.Account)
	}
	return &githubsetup.View{SchemaVersion: 1, Phase: "ready", Account: strings.Join(accounts, ", "), Repositories: append([]string(nil), parsed.GitHubTargets...)}, nil
}

func (h *hostGitHubSetup) beginGitHubInstallationAddition(ctx context.Context, request githubsetup.Request, base []byte, current lifecycle.ManagedIntegrationState) (githubSetupSession, error) {
	var accountID int64
	var accountType string
	var err error
	if request.Account != "" {
		accountID, accountType, err = h.accountIdentity(ctx, request.Account)
		if err != nil {
			return githubSetupSession{}, err
		}
	}
	if request.AccountType != "" && request.AccountType != accountType {
		return githubSetupSession{}, errors.New("requested account type does not match the GitHub account")
	}
	candidate, err := loadManagedGitHubFromStore(ctx, h.Store)
	if err != nil {
		return githubSetupSession{}, err
	}
	defer clear(candidate.KeyRaw)
	if !bytes.Equal(candidate.ConfigRaw, base) || lifecycle.ManagedIntegrationDigest(candidate.KeyRaw) != current.GitHub.CredentialSHA256 {
		return githubSetupSession{}, errors.New("GitHub configuration changed while beginning setup")
	}
	jwt, err := githubapp.AppJWT(candidate.KeyRaw, candidate.Config.GitHubAppID, h.now())
	if err != nil {
		return githubSetupSession{}, err
	}
	var app struct {
		ID    int64  `json:"id"`
		Slug  string `json:"slug"`
		Owner struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
	}
	if err = h.api(ctx, http.MethodGet, "/app", jwt, nil, &app); err != nil {
		return githubSetupSession{}, err
	}
	if app.ID != candidate.Config.GitHubAppID || !setupName(app.Slug, 100) || !setupName(app.Owner.Login, 39) || (app.Owner.Type != "User" && app.Owner.Type != "Organization") {
		return githubSetupSession{}, errors.New("GitHub returned invalid existing App metadata")
	}
	account := request.Account
	var baseline []githubInstallationSnapshot
	if account == "" {
		if app.Owner.ID <= 0 {
			return githubSetupSession{}, errors.New("GitHub returned invalid existing App owner")
		}
		account, accountID, accountType = app.Owner.Login, app.Owner.ID, "user"
		if app.Owner.Type == "Organization" {
			accountType = "organization"
		}
		items, listErr := h.listSetupInstallations(ctx, candidate.KeyRaw, app.ID)
		if listErr != nil {
			return githubSetupSession{}, listErr
		}
		for _, item := range items {
			baseline = append(baseline, githubInstallationSnapshot{ID: item.ID, UpdatedAt: item.UpdatedAt})
		}
	}
	settings := "https://github.com/settings/apps/" + app.Slug + "/advanced"
	if app.Owner.Type == "Organization" {
		settings = "https://github.com/organizations/" + app.Owner.Login + "/settings/apps/" + app.Slug + "/advanced"
	}
	state := make([]byte, 32)
	if _, err = rand.Read(state); err != nil {
		return githubSetupSession{}, err
	}
	session := githubSetupSession{Version: 1, Phase: "installation", State: hex.EncodeToString(state), CreatedAt: h.now(), RedirectURL: request.RedirectURL,
		Account: strings.ToLower(account), AccountType: accountType, OwnerID: accountID, AppID: app.ID, Slug: app.Slug,
		PrivateKey: append([]byte(nil), candidate.KeyRaw...), BaseConfig: append([]byte(nil), base...), AppSettingsURL: settings,
		SelectInstallation: request.Account == "", InstallationBaseline: baseline}
	if err = h.save(ctx, session); err != nil {
		clear(session.PrivateKey)
		return githubSetupSession{}, err
	}
	// Discover a pre-existing installation without reopening its browser page.
	if !session.SelectInstallation {
		err = h.discover(ctx, &session)
	}
	if err == nil {
		err = h.save(ctx, session)
	}
	if err != nil {
		clear(session.PrivateKey)
		return githubSetupSession{}, err
	}
	return session, nil
}

// validateGitHubInstallationAddition protects the transaction from stale setup
// sessions, replacing an App, or overwriting any existing installation or limit.
func validateGitHubInstallationAddition(ctx context.Context, store *lifecycle.FileStore, current lifecycle.ManagedIntegrationState, candidate managedGitHubCandidate) error {
	raw, parsed, err := loadManagedGitHubPublicConfig(ctx, store)
	if err != nil {
		return err
	}
	if candidate.Config.GitHubAppID != parsed.GitHubAppID || lifecycle.ManagedIntegrationDigest(candidate.KeyRaw) != current.GitHub.CredentialSHA256 ||
		len(candidate.Config.GitHubInstallations) != len(parsed.GitHubInstallations)+1 || !bytes.HasPrefix(candidate.ConfigRaw, append(append([]byte(nil), raw...), '\n')) {
		return errors.New("GitHub setup must preserve the existing App, credentials and installations")
	}
	return nil
}
