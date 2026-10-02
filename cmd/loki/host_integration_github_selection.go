package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// Collect approved installations from the App-authenticated API when the user
// finishes browser setup. A legacy Configure URL can still select one verified ID.
func (h *hostGitHubSetup) discoverSelectedInstallation(ctx context.Context, s *githubSetupSession, installationID int64) error {
	items, err := h.listSetupInstallations(ctx, s.PrivateKey, s.AppID)
	if err != nil {
		return err
	}
	base := s.BaseConfig
	existing := map[int64]config.GitHubInstallation{}
	if len(base) > 0 {
		parsed, parseErr := config.ParseGitHubFragment(base)
		if parseErr != nil {
			return parseErr
		}
		for _, installation := range parsed.GitHubInstallations {
			existing[installation.InstallationID] = installation
		}
	} else {
		base = []byte(fmt.Sprintf("github_app_id = %d\ngithub_api_version = \"2026-03-10\"\n", s.AppID))
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Account.Login) < strings.ToLower(items[j].Account.Login)
	})
	raw := append([]byte(nil), base...)
	var selected *setupInstallation
	for i := range items {
		item := &items[i]
		if installationID > 0 && item.ID != installationID {
			continue
		}
		accountType := "user"
		if item.Account.Type == "Organization" {
			accountType = "organization"
		}
		if configured, ok := existing[item.ID]; ok {
			if !strings.EqualFold(configured.Account, item.Account.Login) || configured.AccountType != accountType {
				return errors.New("GitHub installation identity changed")
			}
			if item.SuspendedAt != nil {
				return errors.New("a configured GitHub App installation is suspended")
			}
		} else {
			if item.SuspendedAt != nil {
				if installationID > 0 {
					return errors.New("GitHub App installation is suspended")
				}
				continue
			}
			account := strings.ToLower(item.Account.Login)
			raw = append(raw, []byte(fmt.Sprintf("\n[[github_installations]]\naccount = %s\naccount_type = %s\ninstallation_id = %d\nrepositories = [\"*\"]\n", strconv.Quote(account), strconv.Quote(accountType), item.ID))...)
		}
		selected = item
	}
	if selected == nil {
		if installationID > 0 {
			return errors.New("the selected installation does not belong to this GitHub App")
		}
		if len(existing) == 0 {
			return nil
		}
	}
	parsed, err := config.ParseGitHubFragment(raw)
	if err != nil {
		return errors.New("GitHub setup generated invalid configuration")
	}
	if selected != nil {
		s.Account, s.OwnerID, s.AccountType = strings.ToLower(selected.Account.Login), selected.Account.ID, "user"
		if selected.Account.Type == "Organization" {
			s.AccountType = "organization"
		}
	}
	s.ConfigRaw, s.Repositories, s.Phase = raw, append([]string(nil), parsed.GitHubTargets...), "configured"
	return nil
}
