package mcptransport

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/mcpserver"
)

const (
	integrationUnconfigured = "unconfigured"
	integrationDisabled     = "disabled"
	integrationReady        = "ready"
	integrationDegraded     = "degraded"
)

func effectiveIntegrationState(configured, enabled, ready bool) string {
	switch {
	case ready:
		return integrationReady
	case !configured:
		return integrationUnconfigured
	case !enabled:
		return integrationDisabled
	default:
		return integrationDegraded
	}
}

func (c *SystemController) integrationStatus(ctx context.Context) map[string]any {
	browserConfigured := true
	browserEnabled := c.BrowserSocket != "" && socketExists(c.BrowserSocket)
	browserReady := browserEnabled

	githubConfigured := c.Config.GitHubAppID != 0
	githubEnabled := githubConfigured
	githubReady := githubConfigured && len(c.Config.GitHubTargets) > 0

	signingPublicKey := exists("/home/runner/.ssh/id_ed25519.pub")
	signingAgent := c.SigningSocket != "" && socketExists(c.SigningSocket)
	signingFormat := c.git(ctx, "config", "--global", "--includes", "--get", "gpg.format")
	signingRequired := strings.EqualFold(c.git(ctx, "config", "--global", "--includes", "--get", "commit.gpgsign"), "true")
	signingIdentity := c.git(ctx, "config", "--global", "--includes", "--get", "user.name") != "" &&
		c.git(ctx, "config", "--global", "--includes", "--get", "user.email") != ""
	signingConfigured := signingPublicKey && signingFormat == "ssh" && signingRequired && signingIdentity
	signingEnabled := signingAgent
	signingReady := signingConfigured && signingEnabled

	return map[string]any{
		"browser": map[string]any{
			"supported":  true,
			"configured": browserConfigured,
			"enabled":    browserEnabled,
			"ready":      browserReady,
			"state":      effectiveIntegrationState(browserConfigured, browserEnabled, browserReady),
			"authority":  "this Loki server's managed browser runtime",
		},
		"github": map[string]any{
			"supported":      true,
			"configured":     githubConfigured,
			"enabled":        githubEnabled,
			"ready":          githubReady,
			"state":          effectiveIntegrationState(githubConfigured, githubEnabled, githubReady),
			"authentication": "GitHub App installation tokens",
			"target_count":   len(c.Config.GitHubTargets),
			"authority":      "this Loki server's configured GitHub App installations",
		},
		"signing": map[string]any{
			"supported":  true,
			"configured": signingConfigured,
			"enabled":    signingEnabled,
			"ready":      signingReady,
			"state":      effectiveIntegrationState(signingConfigured, signingEnabled, signingReady),
			"format": func() any {
				if signingFormat == "" {
					return nil
				}
				return signingFormat
			}(),
			"identity_configured":     signingIdentity,
			"commit_signing_required": signingRequired,
			"public_key_available":    signingPublicKey,
			"agent_socket_available":  signingAgent,
			"authority":               "this Loki server's isolated SSH signing agent",
		},
	}
}

func integrationIsReady(status map[string]any, name string) bool {
	raw, ok := status[name].(map[string]any)
	if !ok {
		return false
	}
	ready, _ := raw["ready"].(bool)
	return ready
}

func GitHubUnavailableHandlers() map[string]mcpserver.Handler {
	handler := func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
		return nil, fault.New(
			fault.CodeUnavailable,
			"GitHub integration is not configured for this Loki server",
			false,
			"configure it with `loki integration setup github` and retry",
		)
	}
	return map[string]mcpserver.Handler{
		"github_read":               handler,
		"github_write":              handler,
		"github":                    handler,
		"github_issue_fields_read":  handler,
		"github_issue_fields_write": handler,
	}
}
