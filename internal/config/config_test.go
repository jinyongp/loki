package config

import (
	"strings"
	"testing"
)

func TestDefaultAndCommands(t *testing.T) {
	c, err := Parse([]byte(`root="."
[checks]
lint=["pnpm","lint"]
[processes.dev]
command=["pnpm","dev","--host","127.0.0.1"]
cwd="web"
timeout_seconds=120
max_output_bytes=8192
[processes.dev.environment]
NODE_ENV="development"
[executables]
cargo="/opt/cargo/bin/cargo"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 8765 || c.MaxFileBytes != 16777216 || c.MaxActionProcesses != 12 || c.Checks["lint"].Command[0] != "pnpm" || c.Processes["dev"].Environment["NODE_ENV"] != "development" || c.Processes["dev"].TimeoutSeconds != 120 || c.Executables["cargo"] != "/opt/cargo/bin/cargo" {
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
		`host="0.0.0.0"`, `port=100`, `public_hosts="mcp.example.com"`, `public_hosts=["https://mcp.example.com"]`,
		`cloudflare_access_team_domain="example.cloudflareaccess.com"`, `cloudflare_access_audience="` + strings.Repeat("a", 64) + `"`,
		`preview_access_audience="` + strings.Repeat("b", 64) + `"`, `preview_base_domain="localhost"`,
		`artifact_base_url="http://mcp.example.com/artifacts"`, `artifact_base_url="https://other.example.com/artifacts"`,
		"[executables]\ncargo=\"bin/cargo\"", "[checks]\nbuild=[]", "[checks.build]\ncommand=[\"pnpm\"]\ncwd=\"../escape\"",
		"[checks.build]\ncommand=[\"pnpm\"]\n[checks.build.environment]\nPATH=\"/tmp\"",
		"[checks.build]\ncommand=[\"pnpm\"]\n[checks.build.environment]\nLD_PRELOAD=\"x\"",
		`max_action_processes=2`, `max_action_processes=true`, `max_action_processes="12"`,
	} {
		t.Run(text, func(t *testing.T) {
			if _, err := Parse([]byte(text)); err == nil {
				t.Error("accepted invalid config")
			}
		})
	}
}

func TestCheckedInConfig(t *testing.T) {
	if _, err := Load("../../config/loki-mcp.toml"); err != nil {
		t.Fatal(err)
	}
}
