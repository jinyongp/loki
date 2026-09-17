package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/artifacts"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/portguard"
	"loki/internal/previews"
)

type PreviewController struct {
	Store   *previews.Store
	Runtime RuntimeCaller
	Ports   portguard.Policy
	Inspect func(context.Context, int) (map[string]any, error)
}
type previewRequest struct {
	Action      string
	Port        *int
	Routes      *map[string]int
	Environment map[string]string `json:"environment_routes"`
	TTL         int               `json:"ttl_seconds"`
}

func runtimeDecode(ctx context.Context, client RuntimeCaller, request any, out any) error {
	if client == nil {
		return fault.Error("runtime is unavailable")
	}
	raw, err := client.Call(ctx, request)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return fault.Error("invalid runtime response")
	}
	return nil
}
func (c *PreviewController) listener(ctx context.Context, port int) (map[string]any, error) {
	result, err := InspectWorkspacePort(ctx, c.Ports, c.Inspect, c.Runtime, port)
	if err != nil {
		return nil, err
	}
	if row := portListener(result); row != nil {
		return row, nil
	}
	return nil, fault.Error(fmt.Sprintf("port %d is not a runner-owned workspace development server", port))
}
func (c *PreviewController) PortAllowed(ctx context.Context, port int) bool {
	_, err := c.listener(ctx, port)
	return err == nil
}
func rejectLoopbacks(listener map[string]any) error {
	pid, ok := listener["pid"].(float64)
	if !ok || pid <= 0 {
		return nil
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(int(pid)), "environ"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fault.Error("PREVIEW_ENV_UNREADABLE: publish explicit public routes with action=stack")
	}
	for _, entry := range strings.Split(string(data), "\x00") {
		name, value, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "PUBLIC_") && !strings.HasPrefix(name, "VITE_") && !strings.HasPrefix(name, "NEXT_PUBLIC_") {
			continue
		}
		u, err := url.Parse(value)
		if err != nil {
			continue
		}
		switch u.Hostname() {
		case "127.0.0.1", "localhost", "::1", "0.0.0.0":
			return fault.Error("PREVIEW_LOCAL_URL: publish explicit public routes with action=stack")
		}
	}
	return nil
}
func (c *PreviewController) Publish(ctx context.Context, r previewRequest) (map[string]any, error) {
	if c.Store == nil {
		return nil, fault.Error("temporary live preview sharing is not configured")
	}
	if r.TTL < 60 || r.TTL > 86400 {
		return nil, fault.Error("preview lifetime must be between 60 and 86400 seconds")
	}
	var routes map[string]int
	switch r.Action {
	case "server":
		port, err := mcpserver.Require(r.Port, "port")
		if err != nil {
			return nil, err
		}
		routes = map[string]int{"/": port}
	case "stack":
		var err error
		routes, err = mcpserver.Require(r.Routes, "routes")
		if err != nil {
			return nil, err
		}
	default:
		return nil, fault.Error("preview_publish action must be server or stack")
	}
	if _, err := previews.Normalize(routes); err != nil {
		return nil, err
	}
	var root map[string]any
	for prefix, port := range routes {
		listener, err := c.listener(ctx, port)
		if err != nil {
			return nil, err
		}
		if prefix == "/" {
			root = listener
		}
	}
	if err := rejectLoopbacks(root); err != nil {
		return nil, err
	}
	cwd, _ := root["cwd"].(string)
	if cwd == "" {
		cwd = "/workspace"
	}
	command, _ := root["command"].(string)
	if command == "" {
		command = "unknown"
	}
	return c.Store.Publish(routes, cwd, command, r.TTL)
}

func PreviewHandlers(c *PreviewController, artifactsStore *artifacts.Store) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"preview_publish": mcpserver.Typed(func(ctx context.Context, r previewRequest) (*mcp.CallToolResult, error) {
			return objectResult(c.Publish(ctx, r))
		}),
		"shared_resources": mcpserver.Typed(func(ctx context.Context, r struct{ Kind string }) (*mcp.CallToolResult, error) {
			previewsResult := map[string]any{"previews": []any{}, "configured": false}
			if c.Store != nil {
				previewsResult = map[string]any{"previews": c.Store.List(), "configured": true}
			}
			switch r.Kind {
			case "previews":
				return mcpserver.Object(previewsResult)
			case "artifacts":
				return mcpserver.Object(ArtifactList(artifactsStore))
			case "all":
				return mcpserver.Object(map[string]any{"previews": previewsResult, "artifacts": ArtifactList(artifactsStore)})
			}
			return nil, fault.Error("shared_resources kind must be all, previews, or artifacts")
		}),
		"revoke_share": mcpserver.Typed(func(ctx context.Context, r struct {
			Kind string
			ID   string `json:"share_id"`
		}) (*mcp.CallToolResult, error) {
			if r.Kind == "artifact" {
				return objectResult(ArtifactRevoke(artifactsStore, r.ID))
			}
			if r.Kind != "preview" {
				return nil, fault.Error("revoke_share kind must be preview or artifact")
			}
			if c.Store == nil {
				return nil, fault.Error("temporary live preview sharing is not configured")
			}
			revoked := c.Store.Revoke(r.ID)
			if revoked == nil {
				return nil, fault.Error("preview share was not found or has expired")
			}
			return mcpserver.Object(map[string]any{"revoked": true, "share_id": r.ID, "port": revoked["port"]})
		}),
	}
}
