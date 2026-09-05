package service

import (
	"context"
	"fmt"
	"mime"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/artifacts"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/workspace"
)

type artifactRequest struct {
	Action   string
	Path     *string
	Paths    *[]string
	Filename string
	TTL      int `json:"ttl_seconds"`
}
type shareImageRequest struct {
	Path string
	TTL  int `json:"ttl_seconds"`
}

func ArtifactHandlers(files *workspace.Files, store *artifacts.Store) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"share_image": mcpserver.Typed(func(ctx context.Context, r shareImageRequest) (*mcp.CallToolResult, error) {
			data, metadata, err := files.Image(r.Path)
			if err != nil {
				return nil, err
			}
			if store == nil {
				return nil, fault.Error("temporary image sharing is not configured")
			}
			if r.TTL < 60 || r.TTL > 3600 {
				return nil, fault.Error("image link lifetime must be between 60 and 3600 seconds")
			}
			published, err := store.Publish(data, filepath.Base(r.Path), metadata["mime_type"].(string), metadata["sha256"].(string), r.TTL, "inline")
			if err != nil {
				return nil, err
			}
			url := published["url"].(string)
			metadata["url"], metadata["expires_at"], metadata["display_markdown"] = url, published["expires_at"], "![Loki image]("+url+")"
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Temporary image URL: " + url}}, StructuredContent: metadata}, nil
		}),
		"artifact_publish": mcpserver.Typed(func(ctx context.Context, r artifactRequest) (*mcp.CallToolResult, error) {
			var data []byte
			var path, filename, mimeType string
			var err error
			var extra map[string]any
			switch r.Action {
			case "file":
				path, err = mcpserver.Require(r.Path, "path")
				if err != nil {
					return nil, err
				}
				data, err = files.Attachment(path)
				filename = filepath.Base(path)
				mimeType = strings.Split(mime.TypeByExtension(filepath.Ext(filename)), ";")[0]
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}
			case "bundle":
				paths, e := mcpserver.Require(r.Paths, "paths")
				if e != nil {
					return nil, e
				}
				filename = r.Filename
				path = strings.Join(paths, ",")
				mimeType = "application/zip"
				data, extra, err = files.Bundle(ctx, paths, filename)
			default:
				return nil, fault.Error("artifact_publish action must be file or bundle")
			}
			if err != nil {
				return nil, err
			}
			if store == nil {
				return nil, fault.Error("temporary file sharing is not configured")
			}
			if r.TTL < 60 || r.TTL > 3600 {
				return nil, fault.Error("file link lifetime must be between 60 and 3600 seconds")
			}
			digest := workspace.Digest(data)
			result, err := store.Publish(data, filename, mimeType, digest, r.TTL, "attachment")
			if err != nil {
				return nil, err
			}
			result["path"], result["filename"], result["mime_type"], result["bytes"], result["sha256"] = path, filename, mimeType, len(data), digest
			for k, v := range extra {
				result[k] = v
			}
			url := result["url"].(string)
			size := int64(len(data))
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "Temporary download: " + url},
				&mcp.ResourceLink{Name: filename, URI: url, Description: fmt.Sprintf("Temporary Loki workspace artifact (%d bytes)", len(data)), MIMEType: mimeType, Size: &size},
			}, StructuredContent: result}, nil
		}),
	}
}

func ArtifactList(store *artifacts.Store) map[string]any {
	if store == nil {
		return map[string]any{"artifacts": []any{}, "configured": false}
	}
	return map[string]any{"artifacts": store.List(), "configured": true}
}

func ArtifactRevoke(store *artifacts.Store, id string) (map[string]any, error) {
	if store == nil {
		return nil, fault.Error("temporary file sharing is not configured")
	}
	result := store.Revoke(id)
	if result == nil {
		return nil, fault.Error("artifact share was not found or has expired")
	}
	result["revoked"] = true
	return result, nil
}
