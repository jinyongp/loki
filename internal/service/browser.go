package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image/png"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/integrations/sharing/artifacts"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/work/workspace"
)

func browserCall(ctx context.Context, client BrowserCaller, operation string, args map[string]any) (map[string]any, error) {
	if client == nil {
		return nil, fault.Error("browser is not configured")
	}
	return client.Call(ctx, operation, args)
}
func browserScreenshotMetadata(data []byte, full bool) map[string]any {
	return map[string]any{
		"path": "browser://active-tab", "mime_type": "image/png",
		"bytes": len(data), "sha256": workspace.Digest(data), "full_page": full,
	}
}

func browserShareFingerprint(full bool, ttl int) string {
	payload := "browser-share-screenshot:v1\n" + strconv.FormatBool(full) + "\n" + strconv.Itoa(ttl)
	return workspace.Digest([]byte(payload))
}

func browserShareMetadata(publication map[string]any, full bool) map[string]any {
	url, _ := publication["url"].(string)
	return map[string]any{
		"path": "browser://active-tab", "mime_type": publication["mime_type"],
		"bytes": publication["bytes"], "sha256": publication["sha256"],
		"full_page": full, "share_id": publication["share_id"],
		"url": url, "expires_at": publication["expires_at"],
		"display_markdown": "![Loki image](" + url + ")",
	}
}

func browserShareReplayError(err error) error {
	if errors.Is(err, artifacts.ErrRequestConflict) {
		return fault.New(fault.CodeConflict, "browser screenshot request_id was already used for different share inputs", false, "generate a new request_id when full_page or ttl_seconds changes")
	}
	return err
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
	RequestID string `json:"request_id"`
}

func BrowserHandlers(client BrowserCaller, files *workspace.Files, store *artifacts.Store) map[string]mcpserver.Handler {
	handlers := map[string]mcpserver.Handler{}
	if client == nil {
		return handlers
	}
	for tool, actions := range map[string]map[string]string{
		"browser_session":  {"start": "start", "navigate": "navigate", "back": "back", "forward": "forward", "reload": "reload", "stop_loading": "stop_loading", "stop": "stop"},
		"browser_observe":  {"state": "state", "tabs": "list_tabs", "console": "console", "network": "network", "request": "request", "websockets": "websockets", "errors": "page_errors", "diagnostics": "debug_diagnostics", "dialog": "dialog_state", "downloads": "downloads"},
		"browser_interact": {"click": "click", "hover": "hover", "drag": "drag", "wheel": "wheel", "fill": "fill", "type": "type", "key": "key", "shortcut": "shortcut", "select_option": "select_option", "set_checked": "set_checked", "focus": "focus", "upload": "upload", "dialog": "handle_dialog", "switch_tab": "switch_tab", "close_tab": "close_tab"},
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
			if tool == "browser_interact" && action == "upload" {
				paths, err := browserUploadPaths(args)
				if err != nil {
					return nil, err
				}
				stager, ok := client.(BrowserUploadStager)
				if !ok {
					return nil, fault.Error("browser upload staging is unavailable")
				}
				staged, err := stager.StageBrowserUpload(ctx, files, paths)
				if err != nil {
					return nil, err
				}
				defer staged.Cleanup()
				internalArgs := make(map[string]any, len(args))
				for key, value := range args {
					if key != "paths" {
						internalArgs[key] = value
					}
				}
				internalArgs["staged_files"] = browserUploadReferences(staged)
				result, err := browserCall(ctx, client, operation, internalArgs)
				if err != nil {
					return nil, err
				}
				delete(result, "file_count")
				result["files"] = staged.Files
				return mcpserver.Object(result)
			}
			return objectResult(browserCall(ctx, client, operation, args))
		}
	}
	handlers["browser_screenshot"] = mcpserver.Typed(func(ctx context.Context, r screenshotRequest) (*mcp.CallToolResult, error) {
		data, err := browserScreenshot(ctx, client, r.FullPage)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: data}},
			StructuredContent: browserScreenshotMetadata(data, r.FullPage),
		}, nil
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
	if store != nil {
		handlers["browser_share_screenshot"] = mcpserver.Typed(func(ctx context.Context, r screenshotRequest) (*mcp.CallToolResult, error) {
			if store == nil {
				return nil, fault.Error("temporary image sharing is not configured")
			}
			if !artifacts.ValidRequestID(r.RequestID) {
				return nil, fault.New(fault.CodeInvalidInput, "browser screenshot request_id must be a UUID", false, "generate a new UUID request_id")
			}
			if r.TTL < 60 || r.TTL > 3600 {
				return nil, fault.Error("image link lifetime must be between 60 and 3600 seconds")
			}
			fingerprint := browserShareFingerprint(r.FullPage, r.TTL)
			if replay, ok, err := store.Replay(r.RequestID, fingerprint); err != nil {
				return nil, browserShareReplayError(err)
			} else if ok {
				metadata := browserShareMetadata(replay, r.FullPage)
				url := metadata["url"].(string)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Temporary image URL: " + url}}, StructuredContent: metadata}, nil
			}
			data, err := browserScreenshot(ctx, client, r.FullPage)
			if err != nil {
				return nil, err
			}
			digest := workspace.Digest(data)
			published, err := store.PublishReplay(r.RequestID, fingerprint, data, "browser-screenshot.png", "image/png", digest, r.TTL, "inline")
			if err != nil {
				return nil, browserShareReplayError(err)
			}
			metadata := browserShareMetadata(published, r.FullPage)
			url := metadata["url"].(string)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Temporary image URL: " + url}}, StructuredContent: metadata}, nil
		})

	}
	return handlers
}
