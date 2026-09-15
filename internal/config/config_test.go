package config

import (
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte(`root="."`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Root != "." || c.Port != 8765 || c.MaxFileBytes != 16777216 || c.MaxOutputBytes != 262144 {
		t.Fatalf("config: %#v", c)
	}
}

func TestPublicHostsAndAccess(t *testing.T) {
	c, err := Parse([]byte(`public_hosts=["MCP.Example.com","mcp.example.com"]
artifact_base_url="https://mcp.example.com/artifacts/"
cloudflare_access_team_domain="Example.cloudflareaccess.com"
cloudflare_access_audience="` + strings.Repeat("a", 64) + `"
preview_base_domain="Preview.Example.com."
preview_access_audience="` + strings.Repeat("b", 64) + `"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.PublicHosts) != 1 || c.CloudflareTeamDomain != "example.cloudflareaccess.com" || c.ArtifactBaseURL != "https://mcp.example.com/artifacts" || c.PreviewBaseDomain != "preview.example.com" {
		t.Fatalf("config: %#v", c)
	}
	if _, err = Parse([]byte(`preview_base_domain="example.com"`)); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, text := range []string{
		`host="192.0.2.1"`, `port=100`, `public_hosts="mcp.example.com"`, `public_hosts=["https://mcp.example.com"]`,
		`cloudflare_access_team_domain="example.cloudflareaccess.com"`, `cloudflare_access_audience="` + strings.Repeat("a", 64) + `"`,
		`preview_access_audience="` + strings.Repeat("b", 64) + `"`, `preview_base_domain="localhost"`,
		`artifact_base_url="http://mcp.example.com/artifacts"`, `artifact_base_url="https://other.example.com/artifacts"`,
		`max_file_bytes=2`, `max_patch_files=true`, `max_read_lines="many"`,
	} {
		t.Run(text, func(t *testing.T) {
			if _, err := Parse([]byte(text)); err == nil {
				t.Error("accepted invalid config")
			}
		})
	}
}

func TestCheckedInConfig(t *testing.T) {
	for _, path := range []string{"../../config/loki-mcp.toml", "../../config/loki-go.toml"} {
		if _, err := Load(path); err != nil {
			t.Fatal(path, err)
		}
	}
}

func TestGitHubAppConfiguration(t *testing.T) {
	c, err := Parse([]byte(`github_app_id=123
github_installation_id=456
github_targets=["Owner/Repo","second/project"]
github_max_response_bytes=2097152
github_max_pages=30
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.GitHubAppID != 123 || c.GitHubInstallationID != 456 || c.GitHubAPIVersion != "2026-03-10" ||
		len(c.GitHubTargets) != 2 || c.GitHubTargets[0] != "owner/repo" || c.GitHubMaxResponseBytes != 2097152 || c.GitHubMaxPages != 30 {
		t.Fatalf("GitHub config: %#v", c)
	}
}

func TestInvalidGitHubAppConfiguration(t *testing.T) {
	for _, text := range []string{
		`github_app_id=1`,
		`github_app_id=0
github_installation_id=2
github_targets=["owner/repo"]`,
		`github_app_id=1
github_installation_id=2
github_targets=[]`,
		`github_app_id=1
github_installation_id=2
github_targets=["owner/repo","OWNER/REPO"]`,
		`github_app_id=1
github_installation_id=2
github_targets=["owner/../repo"]`,
		`github_max_pages=2`,
		`github_app_id=1
github_installation_id=2
github_targets=["owner/repo"]
github_max_pages=101`,
		`github_app_id=1
github_installation_id=2
github_targets=["owner/repo"]
github_api_version="latest"`,
	} {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("accepted invalid GitHub config: %s", text)
		}
	}
}
