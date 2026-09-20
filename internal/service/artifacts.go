package service

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/artifacts"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/workspace"
)

type artifactRequest struct {
	Action    string
	Path      *string
	Paths     *[]string
	Filename  string
	TTL       int    `json:"ttl_seconds"`
	RequestID string `json:"request_id"`
}
type shareImageRequest struct {
	Path      string
	TTL       int    `json:"ttl_seconds"`
	RequestID string `json:"request_id"`
}

func shareImageFingerprint(path string, ttl int) string {
	return workspace.Digest([]byte("share-image:v1\n" + path + "\n" + strconv.Itoa(ttl)))
}

func shareImageMetadata(publication map[string]any, path string) map[string]any {
	url, _ := publication["url"].(string)
	return map[string]any{
		"path": path, "mime_type": publication["mime_type"], "bytes": publication["bytes"],
		"sha256": publication["sha256"], "share_id": publication["share_id"],
		"url": url, "expires_at": publication["expires_at"],
		"display_markdown": "![Loki image](" + url + ")",
	}
}

func shareImageReplayError(err error) error {
	if errors.Is(err, artifacts.ErrRequestConflict) {
		return fault.New(fault.CodeConflict, "share_image request_id was already used for different share inputs", false, "generate a new request_id when path or ttl_seconds changes")
	}
	return err
}

func normalizeArtifactPaths(paths []string) ([]string, error) {
	if len(paths) == 0 || len(paths) > 64 {
		return nil, fault.Error("paths must contain between 1 and 64 entries")
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		relative, err := policy.Relative(path)
		if err != nil {
			return nil, err
		}
		if seen[relative] {
			continue
		}
		seen[relative] = true
		result = append(result, relative)
	}
	sort.Strings(result)
	return result, nil
}

func artifactPublishFingerprint(action, path string, paths []string, filename string, ttl int) string {
	payload := "artifact-publish:v1\n" + action + "\n" + path + "\n" + strings.Join(paths, "\n") + "\n" + filename + "\n" + strconv.Itoa(ttl)
	return workspace.Digest([]byte(payload))
}

func artifactPublishReplayError(err error) error {
	if errors.Is(err, artifacts.ErrRequestConflict) {
		return fault.New(fault.CodeConflict, "artifact_publish request_id was already used for different publication inputs", false, "generate a new request_id when action, path(s), filename, or ttl_seconds changes")
	}
	return err
}

func artifactResult(result map[string]any) (*mcp.CallToolResult, error) {
	url, _ := result["url"].(string)
	filename, _ := result["filename"].(string)
	mimeType, _ := result["mime_type"].(string)
	sizeValue, _ := result["bytes"].(int)
	size := int64(sizeValue)
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "Temporary download: " + url},
		&mcp.ResourceLink{Name: filename, URI: url, Description: fmt.Sprintf("Temporary Loki workspace artifact (%d bytes)", sizeValue), MIMEType: mimeType, Size: &size},
	}, StructuredContent: result}, nil
}

func ArtifactHandlers(files *workspace.Files, store *artifacts.Store) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"share_image": mcpserver.Typed(func(ctx context.Context, r shareImageRequest) (*mcp.CallToolResult, error) {
			if store == nil {
				return nil, fault.Error("temporary image sharing is not configured")
			}
			if !artifacts.ValidRequestID(r.RequestID) {
				return nil, fault.New(fault.CodeInvalidInput, "share_image request_id must be a UUID", false, "generate a new UUID request_id")
			}
			if r.TTL < 60 || r.TTL > 3600 {
				return nil, fault.Error("image link lifetime must be between 60 and 3600 seconds")
			}
			relative, err := policy.Relative(r.Path)
			if err != nil {
				return nil, err
			}
			fingerprint := shareImageFingerprint(relative, r.TTL)
			if replay, ok, err := store.Replay(r.RequestID, fingerprint); err != nil {
				return nil, shareImageReplayError(err)
			} else if ok {
				metadata := shareImageMetadata(replay, relative)
				url := metadata["url"].(string)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Temporary image URL: " + url}}, StructuredContent: metadata}, nil
			}
			data, metadata, err := files.Image(relative)
			if err != nil {
				return nil, err
			}
			published, err := store.PublishReplay(
				r.RequestID, fingerprint, data, filepath.Base(relative),
				metadata["mime_type"].(string), metadata["sha256"].(string), r.TTL, "inline",
			)
			if err != nil {
				return nil, shareImageReplayError(err)
			}
			result := shareImageMetadata(published, relative)
			url := result["url"].(string)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Temporary image URL: " + url}}, StructuredContent: result}, nil
		}),
		"artifact_publish": mcpserver.Typed(func(ctx context.Context, r artifactRequest) (*mcp.CallToolResult, error) {
			if store == nil {
				return nil, fault.Error("temporary file sharing is not configured")
			}
			if !artifacts.ValidRequestID(r.RequestID) {
				return nil, fault.New(fault.CodeInvalidInput, "artifact_publish request_id must be a UUID", false, "generate a new UUID request_id")
			}
			if r.TTL < 60 || r.TTL > 3600 {
				return nil, fault.Error("file link lifetime must be between 60 and 3600 seconds")
			}

			var (
				data               []byte
				filename, mimeType string
				fingerprint        string
				extras             map[string]any
				err                error
			)
			switch r.Action {
			case "file":
				requested, requireErr := mcpserver.Require(r.Path, "path")
				if requireErr != nil {
					return nil, requireErr
				}
				path, pathErr := policy.Relative(requested)
				if pathErr != nil {
					return nil, pathErr
				}
				filename = filepath.Base(path)
				fingerprint = artifactPublishFingerprint("file", path, nil, "", r.TTL)
				if replay, ok, replayErr := store.Replay(r.RequestID, fingerprint); replayErr != nil {
					return nil, artifactPublishReplayError(replayErr)
				} else if ok {
					return artifactResult(replay)
				}
				data, err = files.Attachment(path)
				mimeType = strings.Split(mime.TypeByExtension(filepath.Ext(filename)), ";")[0]
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}
				extras = map[string]any{"kind": "file", "path": path}
			case "bundle":
				requested, requireErr := mcpserver.Require(r.Paths, "paths")
				if requireErr != nil {
					return nil, requireErr
				}
				paths, pathErr := normalizeArtifactPaths(requested)
				if pathErr != nil {
					return nil, pathErr
				}
				filename = r.Filename
				if filename == "" {
					filename = "loki-workspace.zip"
				}
				fingerprint = artifactPublishFingerprint("bundle", "", paths, filename, r.TTL)
				if replay, ok, replayErr := store.Replay(r.RequestID, fingerprint); replayErr != nil {
					return nil, artifactPublishReplayError(replayErr)
				} else if ok {
					return artifactResult(replay)
				}
				mimeType = "application/zip"
				var bundleMeta map[string]any
				data, bundleMeta, err = files.Bundle(ctx, paths, filename)
				extras = map[string]any{"kind": "bundle", "paths": paths}
				for key, value := range bundleMeta {
					extras[key] = value
				}
			default:
				return nil, fault.Error("artifact_publish action must be file or bundle")
			}
			if err != nil {
				return nil, err
			}
			digest := workspace.Digest(data)
			result, err := store.PublishReplay(
				r.RequestID, fingerprint, data, filename, mimeType, digest, r.TTL, "attachment", extras,
			)
			if err != nil {
				return nil, artifactPublishReplayError(err)
			}
			return artifactResult(result)
		}),
	}
}

func ArtifactList(store *artifacts.Store) map[string]any {
	if store == nil {
		return map[string]any{"artifacts": []any{}, "configured": false, "complete": true}
	}
	return map[string]any{"artifacts": store.List(), "configured": true, "complete": true}
}

func ArtifactRevoke(store *artifacts.Store, id string) (map[string]any, error) {
	if store == nil {
		return nil, fault.Error("temporary file sharing is not configured")
	}
	if !artifacts.ValidShareID(id) {
		return nil, fault.New(fault.CodeInvalidInput, "artifact share_id is invalid", false, "use a share_id returned by artifact publication or shared_resources")
	}
	_ = store.Revoke(id)
	return map[string]any{"kind": "artifact", "share_id": id, "revoked": true}, nil
}
