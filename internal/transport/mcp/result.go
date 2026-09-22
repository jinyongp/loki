package mcptransport

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/mcpserver"
)

func objectResult(value map[string]any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return nil, err
	}
	return mcpserver.Object(value)
}
