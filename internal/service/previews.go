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
	Action    string
	Port      *int
	Routes    *map[string]int
	RequestID string `json:"request_id"`
	TTL       int    `json:"ttl_seconds"`
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
func previewReplayError(err error) error {
	if errors.Is(err, previews.ErrRequestConflict) {
		return fault.New(fault.CodeConflict, "preview request_id was already used for a different publication", false, "generate a new request_id for changed preview inputs")
	}
	return err
}

func (c *PreviewController) Publish(ctx context.Context, r previewRequest) (map[string]any, error) {
	if c.Store == nil {
		return nil, fault.Error("temporary live preview sharing is not configured")
	}
	if !previews.ValidRequestID(r.RequestID) {
		return nil, fault.New(fault.CodeInvalidInput, "preview request_id must be a UUID", false, "generate a new UUID request_id")
	}
	if r.TTL == 0 {
		r.TTL = 900
	}
	if r.TTL < 60 || r.TTL > 86400 {
		return nil, fault.New(fault.CodeInvalidInput, "preview lifetime must be between 60 and 86400 seconds", false, "choose ttl_seconds between 60 and 86400")
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
		return nil, fault.New(fault.CodeInvalidInput, "preview_publish action must be server or stack", false, "choose action=server or action=stack")
	}
	if replayed, ok, err := c.Store.Replay(r.RequestID, routes, r.TTL); err != nil {
		return nil, previewReplayError(err)
	} else if ok {
		replayed["request_id"] = strings.ToLower(r.RequestID)
		return replayed, nil
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
	published, err := c.Store.PublishReplay(r.RequestID, routes, cwd, command, r.TTL)
	if err != nil {
		return nil, previewReplayError(err)
	}
	published["request_id"] = strings.ToLower(r.RequestID)
	return published, nil
}

func PreviewHandlers(c *PreviewController, artifactsStore *artifacts.Store) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"preview_publish": mcpserver.Typed(func(ctx context.Context, r previewRequest) (*mcp.CallToolResult, error) {
			return objectResult(c.Publish(ctx, r))
		}),
		"shared_resources": mcpserver.Typed(func(ctx context.Context, r struct{ Kind string }) (*mcp.CallToolResult, error) {
			previewsResult := map[string]any{"previews": []any{}, "configured": false, "complete": true}
			if c.Store != nil {
				previewsResult = map[string]any{"previews": c.Store.List(), "configured": true, "complete": true}
			}
			kind := r.Kind
			if kind == "" {
				kind = "all"
			}
			switch kind {
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
			if !previews.ValidShareID(r.ID) {
				return nil, fault.New(fault.CodeInvalidInput, "preview share_id is invalid", false, "use a share_id returned by preview publication or shared_resources")
			}
			_ = c.Store.Revoke(r.ID)
			return mcpserver.Object(map[string]any{"kind": "preview", "revoked": true, "share_id": r.ID})
		}),
	}
}
