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
			tool.Description = strings.TrimSpace(tool.Description) + " Requires this Loki server's managed browser integration to be ready; tool presence alone does not imply runtime readiness."
			if tool.Meta == nil {
				tool.Meta = mcp.Meta{}
			}
			tool.Meta["loki/integration"] = map[string]any{
				"name":      "browser",
				"authority": "this Loki server's managed browser runtime",
				"readiness": "system_inspect action=server",
			}
		case tool.Name == "github" || strings.HasPrefix(tool.Name, "github_"):
			tool.Description = strings.TrimSpace(tool.Description) + " Uses only GitHub App installation-token authority configured in this Loki server; it never uses ambient gh auth credentials."
			if tool.Meta == nil {
				tool.Meta = mcp.Meta{}
			}
			tool.Meta["loki/integration"] = map[string]any{
				"name":      "github",
				"authority": "this Loki server's configured GitHub App installations",
				"readiness": "system_inspect action=server",
			}
		}
	}
}
