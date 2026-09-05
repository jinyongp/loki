package service

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/process"
)

type processInspectRequest struct {
	Action    string
	SessionID *string `json:"session_id"`
	Offset    *int64
	Limit     int
}

// ProcessInspectHandler exposes the ordinary, non-secret process manager.
// Secret action processes stay behind the runtime's redaction boundary.
func ProcessInspectHandler(manager *process.Manager) mcpserver.Handler {
	return mcpserver.Typed(func(_ context.Context, r processInspectRequest) (*mcp.CallToolResult, error) {
		switch r.Action {
		case "list":
			return mcpserver.Object(manager.List())
		case "read":
			id, err := mcpserver.Require(r.SessionID, "session_id")
			if err != nil {
				return nil, err
			}
			return objectResult(manager.Read(id, r.Offset, r.Limit))
		}
		return nil, fault.Error("process_inspect action must be list or read")
	})
}
