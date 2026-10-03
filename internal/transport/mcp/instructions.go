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
			"GitHub repository tools use this Loki server's App installation tokens. Repository-linked personal Projects use optional, explicitly authorized App user tokens; organization Projects use installation tokens. GitHub tools never use ambient gh auth credentials. "+
			"Disabled Loki browser tools are hidden from tools/list; cached browser calls are rejected. Refresh the client's tools after changing browser enable/disable. "+
			"Startup integration hints: loki_browser_available=%t (explicit user opt-in only), github_ready=%t.",
		browserReady, githubReady,
	)
}
