package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/artifacts"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/previews"
)

type PreviewController struct {
	Store        *previews.Store
	Runtime      RuntimeCaller
	Paths        *policy.Workspace
	Inspect      func(context.Context, int) (map[string]any, error)
	ReadyTimeout time.Duration
}
type previewRequest struct {
	Action      string
	Port        *int
	Routes      *map[string]int
	Environment map[string]string `json:"environment_routes"`
	TTL         int               `json:"ttl_seconds"`
	Profile     *string
	ActionName  *string `json:"action_name"`
	CWD         *string
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
	var guarded map[string]any
	var guardErr error
	if c.Inspect != nil {
		guarded, guardErr = c.Inspect(ctx, port)
	}
	first := func(result map[string]any) map[string]any {
		if result["in_use"] != true {
			return nil
		}
		data, err := json.Marshal(result["listeners"])
		if err != nil {
			return nil
		}
		var rows []map[string]any
		if json.Unmarshal(data, &rows) != nil || len(rows) == 0 {
			return nil
		}
		return rows[0]
	}
	if row := first(guarded); row != nil {
		return row, nil
	}
	var docker map[string]any
	if c.Runtime != nil && runtimeDecode(ctx, c.Runtime, map[string]any{"operation": "inspect_docker_port", "port": port}, &docker) == nil {
		if row := first(docker); row != nil {
			return row, nil
		}
	}
	if guardErr != nil {
		return nil, guardErr
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
		return fault.Error("PREVIEW_ENV_UNREADABLE: use preview_publish action=action")
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
			return fault.Error("PREVIEW_LOCAL_URL: use preview_publish action=action with public route mappings")
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
	if r.Action == "action" {
		return c.run(ctx, r)
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
		return nil, fault.Error("preview_publish action must be server, stack, or action")
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
func (c *PreviewController) run(ctx context.Context, r previewRequest) (result map[string]any, err error) {
	profile, err := mcpserver.Require(r.Profile, "profile")
	if err != nil {
		return nil, err
	}
	action, err := mcpserver.Require(r.ActionName, "action_name")
	if err != nil {
		return nil, err
	}
	if r.Routes != nil {
		if _, ok := (*r.Routes)["/"]; ok {
			return nil, fault.Error("the registered action owns the preview root route")
		}
	}
	cwd := "."
	if r.CWD != nil {
		cwd = *r.CWD
	}
	if c.Paths != nil {
		cwd, err = relativeCWD(c.Paths, cwd)
		if err != nil {
			return nil, err
		}
	}
	var prepared struct {
		Token       string `json:"launch_token"`
		Port        int
		Backend     map[string]int    `json:"backend_routes"`
		Environment map[string]string `json:"environment_routes"`
		Suffixes    map[string]string `json:"environment_suffixes"`
		Required    []string          `json:"required_environment"`
	}
	base := map[string]any{"operation": "prepare_action", "profile": profile, "action_name": action, "cwd": cwd}
	if err = runtimeDecode(ctx, c.Runtime, base, &prepared); err != nil {
		return nil, err
	}
	if prepared.Token == "" || prepared.Port < 1024 || prepared.Port > 65535 {
		return nil, fault.Error("invalid prepared action")
	}
	routes := map[string]int{}
	for k, v := range prepared.Backend {
		routes[k] = v
	}
	if r.Routes != nil {
		for k, v := range *r.Routes {
			routes[k] = v
		}
	}
	environment := map[string]string{}
	for k, v := range prepared.Environment {
		environment[k] = v
	}
	for k, v := range r.Environment {
		environment[k] = v
	}
	missing := []string{}
	for _, name := range prepared.Required {
		if _, ok := environment[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fault.Error("PREVIEW_MAPPING_REQUIRED: " + strings.Join(missing, ", "))
	}
	for _, port := range routes {
		if port == prepared.Port {
			return nil, fault.Error("allocated root port conflicts with a preview backend")
		}
		if _, err = c.listener(ctx, port); err != nil {
			return nil, err
		}
	}
	routes["/"] = prepared.Port
	preview, err := c.Store.Publish(routes, filepath.Join("/workspace", cwd), profile+"/"+action, r.TTL)
	if err != nil {
		return nil, err
	}
	id := preview["share_id"].(string)
	sessionID := ""
	completed := false
	defer func() {
		if !completed {
			c.Store.Revoke(id)
			if sessionID != "" {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = c.Runtime.Call(cleanup, map[string]any{"operation": "stop_process", "session_id": sessionID})
			}
		}
	}()
	public := map[string]string{}
	for name, prefix := range environment {
		if _, ok := routes[prefix]; !ok {
			return nil, fault.Error("preview environment route is invalid")
		}
		value := preview["url"].(string)
		if prefix != "/" {
			value += prefix
		}
		public[name] = value + prepared.Suffixes[name]
	}
	base["operation"] = "run_action"
	base["launch_token"] = prepared.Token
	base["public_environment"] = public
	if err = runtimeDecode(ctx, c.Runtime, base, &result); err != nil {
		return nil, err
	}
	sessionID, _ = result["session_id"].(string)
	if sessionID == "" {
		return nil, fault.Error("invalid action session response")
	}
	timeout := c.ReadyTimeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	ready, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for !c.PortAllowed(ready, prepared.Port) {
		select {
		case <-ready.Done():
			return nil, fault.Error("PREVIEW_NOT_READY: frontend did not start listening")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	for _, key := range []string{"share_id", "url", "routes", "expires_at", "display_markdown"} {
		result[key] = preview[key]
	}
	result["public_environment"] = public
	completed = true
	return result, nil
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
