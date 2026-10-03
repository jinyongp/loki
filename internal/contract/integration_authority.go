package contract

import (
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func applyIntegrationAuthorityMetadata(tools []*mcp.Tool) {
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		switch {
		case strings.HasPrefix(tool.Name, "browser_"):
			tool.Title = "Loki Browser: " + strings.ReplaceAll(strings.TrimPrefix(tool.Name, "browser_"), "_", " ") + " (explicit opt-in)"
			tool.Description = strings.TrimSpace(tool.Description) + " Requires this Loki server's managed browser integration to be ready; tool presence alone does not imply runtime readiness."
			if tool.Meta == nil {
				tool.Meta = mcp.Meta{}
			}
			tool.Meta["loki/integration"] = map[string]any{
				"name":      "browser",
				"authority": "this Loki server's managed browser runtime",
				"readiness": "system_inspect action=server",
				"selection": "explicit user request for the Loki browser",
			}
		case tool.Name == "github" || strings.HasPrefix(tool.Name, "github_"):
			authority := "this Loki server's configured GitHub App installations"
			guidance := " Uses only GitHub App installation-token authority configured in this Loki server; it never uses ambient gh auth credentials."
			if tool.Name == "github" {
				authority = "this Loki server's GitHub App installations and optional user authorization for repository-linked personal Projects"
				guidance = " Uses only this Loki server's configured GitHub App authority, including optional user authorization for repository-linked personal Projects; it never uses ambient gh auth credentials."
			}
			tool.Description = strings.TrimSpace(tool.Description) + guidance
			if tool.Meta == nil {
				tool.Meta = mcp.Meta{}
			}
			tool.Meta["loki/integration"] = map[string]any{
				"name":      "github",
				"authority": authority,
				"readiness": "system_inspect action=server",
			}
		}
	}
}
