package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
)

// AvailableTools hides an optional integration at discovery time and rejects
// cached calls before invoking its handlers. Availability is read per request,
// so lifecycle enable/disable does not require restarting the MCP process.
func AvailableTools(names []string, available func() bool, unavailable error) mcp.Middleware {
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		selected[name] = true
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				call, ok := req.(*mcp.CallToolRequest)
				if ok && selected[call.Params.Name] && !available() {
					return errorResult(fault.Describe(unavailable)), nil
				}
			}
			result, err := next(ctx, method, req)
			if err == nil && method == "tools/list" && !available() {
				if listed, ok := result.(*mcp.ListToolsResult); ok {
					filtered := *listed
					filtered.Tools = make([]*mcp.Tool, 0, len(listed.Tools))
					for _, tool := range listed.Tools {
						if !selected[tool.Name] {
							filtered.Tools = append(filtered.Tools, tool)
						}
					}
					result = &filtered
				}
			}
			return result, err
		}
	}
}
