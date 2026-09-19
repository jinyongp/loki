package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/png"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/artifacts"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/workspace"
)

func browserCall(ctx context.Context, client BrowserCaller, operation string, args map[string]any) (map[string]any, error) {
	if client == nil {
		return nil, fault.Error("browser is not configured")
	}
	return client.Call(ctx, operation, args)
}
func browserScreenshot(ctx context.Context, client BrowserCaller, full bool) ([]byte, error) {
	result, err := browserCall(ctx, client, "screenshot", map[string]any{"full_page": full})
	if err != nil {
		return nil, err
	}
	encoded, ok := result["data_base64"].(string)
	if !ok || len(encoded) > base64.StdEncoding.EncodedLen(workspace.MaxImageBytes) || strings.ContainsAny(encoded, "\r\n\t ") {
		return nil, fault.Error("browser screenshot response is invalid")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) > workspace.MaxImageBytes {
		return nil, fault.Error("browser screenshot response is invalid")
	}
	if _, err = png.DecodeConfig(bytes.NewReader(data)); err != nil {
		return nil, fault.Error("browser screenshot response is invalid")
	}
	return data, nil
}

type screenshotRequest struct {
	FullPage  bool `json:"full_page"`
	Path      string
	Overwrite bool
	Expected  string `json:"expected_sha256"`
	TTL       int    `json:"ttl_seconds"`
}

func BrowserHandlers(client BrowserCaller, files *workspace.Files, store *artifacts.Store) map[string]mcpserver.Handler {
	handlers := map[string]mcpserver.Handler{}
	for tool, actions := range map[string]map[string]string{
		"browser_session":  {"start": "start", "navigate": "navigate", "stop": "stop"},
		"browser_observe":  {"state": "state", "tabs": "list_tabs", "console": "console", "network": "network", "request": "request", "websockets": "websockets", "errors": "page_errors", "diagnostics": "debug_diagnostics"},
		"browser_interact": {"click": "click", "hover": "hover", "drag": "drag", "wheel": "wheel", "fill": "fill", "type": "type", "key": "key", "shortcut": "shortcut", "select_option": "select_option", "set_checked": "set_checked", "focus": "focus", "back": "back", "switch_tab": "switch_tab", "close_tab": "close_tab"},
	} {
		handlers[tool] = func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
			action, _ := args["action"].(string)
			if tool == "browser_observe" && action == "" {
				action = "state"
			}
			operation := actions[action]
			if operation == "" {
				return nil, fault.Error("invalid " + tool + " action")
			}
			return objectResult(browserCall(ctx, client, operation, args))
		}
	}
	handlers["browser_screenshot"] = mcpserver.Typed(func(ctx context.Context, r screenshotRequest) (*mcp.CallToolResult, error) {
		data, err := browserScreenshot(ctx, client, r.FullPage)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: data}}}, nil
	})
	handlers["browser_save_screenshot"] = mcpserver.Typed(func(ctx context.Context, r screenshotRequest) (*mcp.CallToolResult, error) {
		if _, err := files.Policy.Resolve(r.Path, false); err != nil {
			return nil, err
		}
		if strings.ToLower(filepath.Ext(r.Path)) != ".png" {
			return nil, fault.Error("browser screenshot path must use a .png extension")
		}
		data, err := browserScreenshot(ctx, client, r.FullPage)
		if err != nil {
			return nil, err
		}
		return objectResult(files.SaveScreenshot(r.Path, base64.StdEncoding.EncodeToString(data), r.Overwrite, r.Expected, r.FullPage))
	})
	handlers["browser_share_screenshot"] = mcpserver.Typed(func(ctx context.Context, r screenshotRequest) (*mcp.CallToolResult, error) {
		if store == nil {
			return nil, fault.Error("temporary image sharing is not configured")
		}
		if r.TTL < 60 || r.TTL > 3600 {
			return nil, fault.Error("image link lifetime must be between 60 and 3600 seconds")
		}
		data, err := browserScreenshot(ctx, client, r.FullPage)
		if err != nil {
			return nil, err
		}
		metadata := map[string]any{"path": "browser://active-tab", "mime_type": "image/png", "bytes": len(data), "sha256": workspace.Digest(data), "full_page": r.FullPage}
		published, err := store.Publish(data, "browser-screenshot.png", "image/png", metadata["sha256"].(string), r.TTL, "inline")
		if err != nil {
			return nil, err
		}
		url := published["url"].(string)
		metadata["url"], metadata["expires_at"], metadata["display_markdown"] = url, published["expires_at"], "![Loki image]("+url+")"
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Temporary image URL: " + url}}, StructuredContent: metadata}, nil
	})
	return handlers
}
