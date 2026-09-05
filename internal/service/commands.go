package service

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/commands"
	"loki/internal/fault"
	"loki/internal/mcpserver"
)

func CommandHandlers(c *commands.Controller, portClient RuntimeCaller) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"command_run": mcpserver.Typed(func(ctx context.Context, r commands.Request) (*mcp.CallToolResult, error) {
			return objectResult(c.Run(ctx, r))
		}),
		"command_start": mcpserver.Typed(func(ctx context.Context, r commands.Request) (*mcp.CallToolResult, error) {
			return objectResult(c.Start(ctx, r))
		}),
		"runtime_stop": mcpserver.Typed(func(ctx context.Context, r struct {
			Action  string
			Port    *int
			Session *string `json:"session_id"`
		}) (*mcp.CallToolResult, error) { switch r.Action {
		case "process":
			id, err := mcpserver.Require(r.Session, "session_id")
			if err != nil {
				return nil, err
			}
			return objectResult(c.Manager.Stop(id))
		case "port":
			port, err := mcpserver.Require(r.Port, "port")
			if err != nil {
				return nil, err
			}
			return runtimeObject(ctx, portClient, map[string]any{"operation": "stop", "port": port})
		}; return nil, fault.Error("runtime_stop action must be port or process") }),
	}
}
