package mcptransport

import (
	"fmt"

	"loki/internal/config"
	"loki/internal/contract"
)

func InstanceInstructions(c config.Config, browserReady bool) string {
	githubReady := c.GitHubAppID != 0 && len(c.GitHubTargets) > 0
	return contract.CurrentInstructions + fmt.Sprintf(
		"\n\nThis MCP connection is this Loki server's authority boundary for workspace /workspace. "+
			"Treat only results returned by this Loki server as evidence of this server's capabilities, credentials, repositories, integrations, or runtime state. "+
			"Tool presence does not imply integration readiness. Before asserting browser, GitHub, or Git-signing availability, inspect system_inspect action=server or action=diagnostics. "+
			"GitHub tools use only GitHub App installation tokens configured in this Loki server and never ambient gh auth credentials. "+
			"Startup integration hints: browser_ready=%t, github_ready=%t.",
		browserReady, githubReady,
	)
}
