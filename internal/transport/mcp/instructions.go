package mcptransport

import (
	"fmt"

	"loki/internal/config"
	"loki/internal/contract"
)

func InstanceInstructions(c config.Config, browserReady bool) string {
	githubReady := c.GitHubAppID != 0 && len(c.GitHubTargets) > 0
	return contract.CurrentInstructions + fmt.Sprintf(
		"\n\nWorkspace %q on the selected execution host is this Loki server's authority boundary. "+
			"Treat only results returned by this Loki server as evidence of this server's capabilities, credentials, repositories, integrations, or runtime state. "+
			"Tool presence does not imply integration readiness. Use the selected host's loki tools status or loki tools doctor; system_inspect is available only in compositions that register it. "+
			"GitHub repository tools and repository-linked Projects use this Loki server's App installation tokens by default. The personal-projects capability optionally selects explicitly authorized App user tokens for personal-account Projects. GitHub tools never use ambient gh auth credentials. "+
			"Official browser engines own separate project sessions, tabs and login state from the desktop app's in-app browser. Use this connection for browser work when the user selects it. Disabled groups leave discovery and cached calls are rejected. "+
			"Startup integration hints: official_browser_selected=%t, github_configured=%t; these are configuration hints, not runtime acceptance.",
		c.Root, browserReady, githubReady,
	)
}
