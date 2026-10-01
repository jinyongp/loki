package config

import (
	"strings"
	"testing"
)

func TestGitHubAllRepositoryConfiguration(t *testing.T) {
	raw := `github_app_id=123
[[github_installations]]
account="Owner"
account_type="organization"
installation_id=456
repositories=["*"]
`
	c, err := Parse([]byte(raw))
	if err != nil || len(c.GitHubTargets) != 1 || c.GitHubTargets[0] != "owner/*" {
		t.Fatalf("config=%+v err=%v", c, err)
	}
	for _, repositories := range []string{`["*", "repo"]`, `["**"]`, `["repo*"]`, `["*/other"]`} {
		if _, err := Parse([]byte(strings.Replace(raw, `["*"]`, repositories, 1))); err == nil {
			t.Fatalf("invalid selection accepted: %s", repositories)
		}
	}
}
