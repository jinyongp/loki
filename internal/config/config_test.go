package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte(`root="."`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Root != "." || c.Port != 18765 || c.MaxFileBytes != 16777216 || c.MaxOutputBytes != 262144 ||
		c.BrowserMaxUploadFiles != 20 || c.BrowserMaxUploadBytes != 67108864 {
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
		`max_file_bytes=2`, `max_patch_files=true`, `max_read_lines="many"`, `browser_max_upload_files=0`, `browser_max_upload_bytes=1024`,
	} {
		t.Run(text, func(t *testing.T) {
			if _, err := Parse([]byte(text)); err == nil {
				t.Error("accepted invalid config")
			}
		})
	}
}

func TestStrictConfigurationInput(t *testing.T) {
	for _, text := range []string{
		`max_processes=1`,
		`max_output_byte=8192`,
		`max_output_bytes=8192.9`,
		`max_output_bytes="8192"`,
		`[executables]\nnode="/unused"`,
		`[checks]\ntest=["false"]`,
	} {
		t.Run(text, func(t *testing.T) {
			if _, err := Parse([]byte(text)); err == nil {
				t.Fatal("accepted unsupported or coerced configuration")
			}
		})
	}
}

func TestCheckedInConfig(t *testing.T) {
	if _, err := Load("../../config/loki-go.toml"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("testdata/python-v047.toml"); err == nil || !strings.Contains(err.Error(), "unsupported Loki configuration setting") {
		t.Fatalf("legacy Python configuration was not rejected: %v", err)
	}
}

func TestGitHubAppConfiguration(t *testing.T) {
	c, err := Parse([]byte(`github_app_id=123
github_max_response_bytes=2097152
github_max_pages=30
github_command_timeout_seconds=90
github_max_input_bytes=8192
github_max_output_bytes=16384
[[github_installations]]
account="Owner"
account_type="organization"
installation_id=456
repositories=["Repo","Second"]
[[github_installations]]
account="Person"
account_type="user"
installation_id=789
repositories=["Project"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.GitHubAppID != 123 || c.GitHubAPIVersion != "2026-03-10" || len(c.GitHubInstallations) != 2 ||
		c.GitHubInstallations[0].Account != "owner" || c.GitHubInstallations[1].AccountType != "user" ||
		len(c.GitHubTargets) != 3 || c.GitHubTargets[2] != "person/project" || c.GitHubMaxResponseBytes != 2097152 ||
		c.GitHubMaxPages != 30 || c.GitHubCommandTimeoutSeconds != 90 || c.GitHubMaxInputBytes != 8192 || c.GitHubMaxOutputBytes != 16384 {
		t.Fatalf("GitHub config: %#v", c)
	}
}

func TestInvalidGitHubAppConfiguration(t *testing.T) {
	for _, text := range []string{
		`github_app_id=1`,
		`github_app_id=0
[[github_installations]]
account="owner"
account_type="organization"
installation_id=2
repositories=["repo"]`,
		`github_app_id=1
github_installations=[]`,
		`github_app_id=1
[[github_installations]]
account="owner"
account_type="team"
installation_id=2
repositories=["repo"]`,
		`github_app_id=1
[[github_installations]]
account="owner"
account_type="organization"
installation_id=2
repositories=[]`,
		`github_app_id=1
[[github_installations]]
account="owner"
account_type="organization"
installation_id=2
repositories=["repo","REPO"]`,
		`github_app_id=1
[[github_installations]]
account="owner"
account_type="organization"
installation_id=2
repositories=["../repo"]`,
		`github_max_pages=2`,
		`github_app_id=1
[[github_installations]]
account="owner"
account_type="organization"
installation_id=2
repositories=["repo"]
github_max_pages=101`,
		`github_app_id=1
[[github_installations]]
account="owner"
account_type="organization"
installation_id=2
repositories=["repo"]
github_api_version="latest"`,
	} {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("accepted invalid GitHub config: %s", text)
		}
	}
}

func TestGitHubInstallationBounds(t *testing.T) {
	var tooManyInstallations strings.Builder
	tooManyInstallations.WriteString("github_app_id=1\n")
	for index := 1; index <= 17; index++ {
		fmt.Fprintf(&tooManyInstallations, "[[github_installations]]\naccount=\"owner%d\"\naccount_type=\"organization\"\ninstallation_id=%d\nrepositories=[\"repo\"]\n", index, index)
	}
	if _, err := Parse([]byte(tooManyInstallations.String())); err == nil {
		t.Fatal("accepted too many GitHub installations")
	}

	var tooManyRepositories strings.Builder
	tooManyRepositories.WriteString("github_app_id=1\n[[github_installations]]\naccount=\"owner\"\naccount_type=\"organization\"\ninstallation_id=1\nrepositories=[")
	for index := 0; index < 65; index++ {
		if index != 0 {
			tooManyRepositories.WriteByte(',')
		}
		fmt.Fprintf(&tooManyRepositories, "\"repo%d\"", index)
	}
	tooManyRepositories.WriteString("]\n")
	if _, err := Parse([]byte(tooManyRepositories.String())); err == nil {
		t.Fatal("accepted too many GitHub repositories")
	}
}

func TestLoadWithGitHub(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "loki.toml")
	github := filepath.Join(directory, "github.toml")
	if err := os.WriteFile(base, []byte(`root="/workspace"`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(github, []byte(`github_app_id=1
[[github_installations]]
account="person"
account_type="user"
installation_id=2
repositories=["repo"]
`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadWithGitHub(base, github)
	if err != nil || c.Root != "/workspace" || len(c.GitHubInstallations) != 1 || c.GitHubTargets[0] != "person/repo" {
		t.Fatalf("merged config: %#v %v", c, err)
	}
	if err := os.WriteFile(github, []byte(`root="/escape"`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadWithGitHub(base, github); err == nil {
		t.Fatal("GitHub fragment changed core configuration")
	}
	if err := os.WriteFile(github, []byte(`github_app_id=2`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, []byte(`github_app_id=1`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadWithGitHub(base, github); err == nil {
		t.Fatal("duplicate GitHub setting accepted")
	}
}
