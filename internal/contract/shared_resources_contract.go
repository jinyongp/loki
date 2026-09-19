package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func previewShareSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"share_id":         map[string]any{"type": "string", "pattern": "^[0-9a-f]{16}$"},
			"url":              map[string]any{"type": "string"},
			"port":             map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
			"routes":           map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}},
			"cwd":              map[string]any{"type": "string"},
			"command":          map[string]any{"type": "string"},
			"created_at":       map[string]any{"type": "string"},
			"expires_at":       map[string]any{"type": "string"},
			"display_markdown": map[string]any{"type": "string"},
		},
		"required": []string{"share_id", "url", "port", "routes", "cwd", "command", "created_at", "expires_at", "display_markdown"},
	}
}

func artifactShareSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"share_id":    map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{43}$"},
			"url":         map[string]any{"type": "string"},
			"expires_at":  map[string]any{"type": "string"},
			"filename":    map[string]any{"type": "string"},
			"mime_type":   map[string]any{"type": "string"},
			"bytes":       map[string]any{"type": "integer", "minimum": 0},
			"sha256":      map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"disposition": map[string]any{"type": "string", "enum": []string{"inline", "attachment"}},
		},
		"required": []string{"share_id", "url", "expires_at", "filename", "mime_type", "bytes", "sha256", "disposition"},
	}
}

func previewResourcesSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"previews":   map[string]any{"type": "array", "items": previewShareSchema()},
			"configured": map[string]any{"type": "boolean"},
			"complete":   map[string]any{"const": true},
		},
		"required": []string{"previews", "configured", "complete"},
	}
}

func artifactResourcesSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"artifacts":  map[string]any{"type": "array", "items": artifactShareSchema()},
			"configured": map[string]any{"type": "boolean"},
			"complete":   map[string]any{"const": true},
		},
		"required": []string{"artifacts", "configured", "complete"},
	}
}

func overrideSharedResources(tool *mcp.Tool) error {
	tool.Description = "List all active temporary preview and artifact shares. Results are complete for the configured share stores and state whether each share type is configured."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "shared_resourcesArguments", "additionalProperties": false,
		"properties": map[string]any{
			"kind": map[string]any{
				"type": "string", "enum": []string{"all", "previews", "artifacts"}, "default": "all",
				"description": "Resource kind to list; omitted means all preview and artifact shares.",
			},
		},
	}
	tool.OutputSchema = map[string]any{
		"type": "object",
		"oneOf": []any{
			previewResourcesSchema(),
			artifactResourcesSchema(),
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"previews":  previewResourcesSchema(),
					"artifacts": artifactResourcesSchema(),
				},
				"required": []string{"previews", "artifacts"},
			},
		},
	}
	return nil
}
