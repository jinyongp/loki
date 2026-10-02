package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"loki/internal/config"
	githubapp "loki/internal/integrations/github"
)

type githubInstallationSnapshot struct {
	ID        int64     `json:"id"`
	UpdatedAt time.Time `json:"updated_at"`
}

type setupInstallation struct {
	ID      int64 `json:"id"`
	AppID   int64 `json:"app_id"`
	Account struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	Selection   string     `json:"repository_selection"`
	UpdatedAt   time.Time  `json:"updated_at"`
	SuspendedAt *time.Time `json:"suspended_at"`
}

func (h *hostGitHubSetup) listSetupInstallations(ctx context.Context, key []byte, appID int64) ([]setupInstallation, error) {
	jwt, err := githubapp.AppJWT(key, appID, h.now())
	if err != nil {
		return nil, err
	}
	var result []setupInstallation
	seen := map[int64]bool{}
	for page := 1; page <= 16; page++ {
		var items []setupInstallation
		if err = h.api(ctx, "GET", fmt.Sprintf("/app/installations?per_page=100&page=%d", page), jwt, nil, &items); err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.AppID != appID || item.ID <= 0 || item.Account.ID <= 0 || !setupName(item.Account.Login, 39) || (item.Account.Type != "User" && item.Account.Type != "Organization") || (item.Selection != "all" && item.Selection != "selected") || seen[item.ID] {
				return nil, errors.New("GitHub returned invalid installation metadata")
			}
			seen[item.ID] = true
			result = append(result, item)
		}
		if len(items) < 100 {
			return result, nil
		}
	}
	return nil, errors.New("GitHub installation listing is incomplete")
}

// Infer the installation selected in GitHub from authenticated API changes,
// never from the App owner or from an unverified browser query parameter.
func (h *hostGitHubSetup) discoverSelectedInstallation(ctx context.Context, s *githubSetupSession, installationID int64) error {
	items, err := h.listSetupInstallations(ctx, s.PrivateKey, s.AppID)
	if err != nil {
		return err
	}
	baseline := map[int64]time.Time{}
	for _, item := range s.InstallationBaseline {
		baseline[item.ID] = item.UpdatedAt
	}
	var chosen *setupInstallation
	for i := range items {
		item := &items[i]
		if installationID > 0 {
			if item.ID != installationID {
				continue
			}
		} else {
			if previous, exists := baseline[item.ID]; exists && previous.Equal(item.UpdatedAt) {
				continue
			}
		}
		if item.SuspendedAt != nil {
			return errors.New("GitHub App installation is suspended")
		}
		if chosen != nil {
			return errors.New("multiple GitHub installations changed during setup; use integration import with an explicit configuration to select accounts")
		}
		chosen = item
	}
	if chosen == nil {
		if installationID > 0 {
			return errors.New("the selected installation does not belong to this GitHub App")
		}
		return nil
	}
	accountType := "user"
	if chosen.Account.Type == "Organization" {
		accountType = "organization"
	}
	base := s.BaseConfig
	if len(base) > 0 {
		parsed, parseErr := config.ParseGitHubFragment(base)
		if parseErr != nil {
			return parseErr
		}
		for _, existing := range parsed.GitHubInstallations {
			if existing.InstallationID == chosen.ID {
				if !strings.EqualFold(existing.Account, chosen.Account.Login) || existing.AccountType != accountType {
					return errors.New("GitHub installation identity changed")
				}
				s.ConfigRaw = append([]byte(nil), base...)
				s.Phase = "configured"
				return nil
			}
		}
	} else {
		base = []byte(fmt.Sprintf("github_app_id = %d\ngithub_api_version = \"2026-03-10\"\n", s.AppID))
	}
	account := strings.ToLower(chosen.Account.Login)
	s.ConfigRaw = append(append([]byte(nil), base...), []byte(fmt.Sprintf("\n[[github_installations]]\naccount = %s\naccount_type = %s\ninstallation_id = %d\nrepositories = [\"*\"]\n", strconv.Quote(account), strconv.Quote(accountType), chosen.ID))...)
	if _, err = config.ParseGitHubFragment(s.ConfigRaw); err != nil {
		return errors.New("GitHub setup generated invalid configuration")
	}
	s.Account, s.AccountType, s.OwnerID = account, accountType, chosen.Account.ID
	s.Repositories, s.Phase = []string{account + "/*"}, "configured"
	return nil
}
