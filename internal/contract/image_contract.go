package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func imageMetadataSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "minLength": 1},
			"mime_type": map[string]any{
				"type": "string", "enum": []string{"image/png", "image/jpeg", "image/webp", "image/gif"},
			},
			"bytes":  map[string]any{"type": "integer", "minimum": 1, "maximum": 10485760},
			"sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
		},
		"required": []string{"path", "mime_type", "bytes", "sha256"},
	}
}

func overrideReadImage(tool *mcp.Tool) error {
	tool.Description = "Read one bounded workspace PNG, JPEG, WebP, or GIF and return MCP image content plus closed typed metadata."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "read_imageArguments", "additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 4096,
				"description": "Workspace-relative image path. Secret-bearing and symlinked paths remain subject to workspace policy.",
			},
		},
		"required": []string{"path"},
	}
	tool.OutputSchema = imageMetadataSchema()
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = true
		tool.Annotations.DestructiveHint = boolPointer(false)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(false)
	}
	return nil
}

func shareImageOutputSchema() map[string]any {
	base := imageMetadataSchema()
	properties := base["properties"].(map[string]any)
	properties["share_id"] = map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{43}$"}
	properties["url"] = map[string]any{"type": "string"}
	properties["expires_at"] = map[string]any{"type": "string"}
	properties["display_markdown"] = map[string]any{"type": "string"}
	required := append([]string(nil), base["required"].([]string)...)
	base["required"] = append(required, "share_id", "url", "expires_at", "display_markdown")
	return base
}

func overrideShareImage(tool *mcp.Tool) error {
	tool.Description = "Publish one workspace image as a temporary inline share. request_id makes retries replay the original share identity instead of creating duplicate links."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "share_imageArguments", "additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 4096,
				"description": "Workspace-relative image path to publish.",
			},
			"request_id": RequestIDSchema("Caller-owned UUID used to replay one image share without creating duplicate temporary links."),
			"ttl_seconds": map[string]any{
				"type": "integer", "minimum": 60, "maximum": 3600, "default": 900,
				"description": "Temporary image-link lifetime in seconds; request-ID replay is retained for this lifetime.",
			},
		},
		"required": []string{"path", "request_id"},
	}
	tool.OutputSchema = shareImageOutputSchema()
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = false
		tool.Annotations.DestructiveHint = boolPointer(false)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(true)
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"share": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryNone,
			AffectedResourceLimit: 1, RecoveryReference: "replay the same request_id while retained or inspect shared_resources kind=artifacts",
		},
	})
}

func writeImageOutputSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":  map[string]any{"type": "string", "minLength": 1},
			"bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": 10485760},
			"mime_type": map[string]any{
				"type": "string", "enum": []string{"image/png", "image/jpeg", "image/webp", "image/gif"},
			},
			"sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"previous_revision": map[string]any{
				"anyOf": []any{
					map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
					map[string]any{"type": "null"},
				},
			},
		},
		"required": []string{"path", "bytes", "mime_type", "sha256", "previous_revision"},
	}
}

func overrideWriteImage(tool *mcp.Tool) error {
	path := map[string]any{
		"type": "string", "minLength": 1, "maxLength": 4096,
		"description": "Workspace-relative destination path whose extension must match mime_type.",
	}
	data := map[string]any{
		"type": "string", "maxLength": 13981016,
		"description": "Base64 image bytes without whitespace; decoded content is bounded to 10 MiB and validated against mime_type and path extension.",
	}
	mime := map[string]any{
		"type": "string", "enum": []string{"image/png", "image/jpeg", "image/webp", "image/gif"},
		"description": "Image MIME type. It must match both the file extension and decoded image signature.",
	}
	overwrite := map[string]any{
		"type": "boolean", "default": false,
		"description": "Allow replacing an existing image only when expected_sha256 matches the current content.",
	}
	expected := map[string]any{
		"type": "string", "pattern": "^[0-9a-f]{64}$",
		"description": "Observed SHA-256 of the current destination; required only for overwrite=true and rejected for create-only writes.",
	}
	tool.Description = "Decode and atomically write one bounded workspace image. Create-only writes reject overwrite guards; overwrite=true requires expected_sha256 and captures the previous revision."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "write_imageArguments", "additionalProperties": false,
		"properties": map[string]any{
			"path": path, "data_base64": data, "mime_type": mime,
			"overwrite": overwrite, "expected_sha256": expected,
		},
		"required": []string{"path", "data_base64", "mime_type"},
		"oneOf": []any{
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"path": path, "data_base64": data, "mime_type": mime,
					"overwrite": map[string]any{"type": "boolean", "const": false},
				},
				"required": []string{"path", "data_base64", "mime_type"},
			},
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"path": path, "data_base64": data, "mime_type": mime,
					"overwrite":       map[string]any{"type": "boolean", "const": true},
					"expected_sha256": expected,
				},
				"required": []string{"path", "data_base64", "mime_type", "overwrite", "expected_sha256"},
			},
		},
	}
	tool.OutputSchema = writeImageOutputSchema()
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = false
		tool.Annotations.DestructiveHint = boolPointer(true)
		tool.Annotations.IdempotentHint = false
		tool.Annotations.OpenWorldHint = boolPointer(false)
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"write": {
			Replay: ReplayUnsafe, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "workspace_read action=file",
		},
	})
}
