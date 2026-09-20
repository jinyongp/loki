package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func browserScreenshotBaseInput() map[string]any {
	return map[string]any{
		"full_page": map[string]any{
			"type": "boolean", "default": false,
			"description": "Capture the full scrollable page when true; otherwise capture the current viewport.",
		},
	}
}

func browserScreenshotMetadataSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":      map[string]any{"const": "browser://active-tab"},
			"mime_type": map[string]any{"const": "image/png"},
			"bytes":     map[string]any{"type": "integer", "minimum": 1, "maximum": 10485760},
			"sha256":    map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"full_page": map[string]any{"type": "boolean"},
		},
		"required": []string{"path", "mime_type", "bytes", "sha256", "full_page"},
	}
}

func overrideBrowserScreenshot(tool *mcp.Tool) error {
	tool.Description = "Capture a bounded PNG screenshot of the active browser tab and return both image content and closed typed metadata."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "browser_screenshotArguments", "additionalProperties": false,
		"properties": browserScreenshotBaseInput(),
	}
	tool.OutputSchema = browserScreenshotMetadataSchema()
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = true
		tool.Annotations.DestructiveHint = boolPointer(false)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(true)
	}
	return nil
}

func browserSaveScreenshotOutputSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":              map[string]any{"type": "string", "minLength": 1},
			"bytes":             map[string]any{"type": "integer", "minimum": 1, "maximum": 10485760},
			"mime_type":         map[string]any{"const": "image/png"},
			"sha256":            map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"previous_revision": map[string]any{"anyOf": []any{map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}, map[string]any{"type": "null"}}},
			"full_page":         map[string]any{"type": "boolean"},
		},
		"required": []string{"path", "bytes", "mime_type", "sha256", "previous_revision", "full_page"},
	}
}

func overrideBrowserSaveScreenshot(tool *mcp.Tool) error {
	fullPage := browserScreenshotBaseInput()["full_page"].(map[string]any)
	path := map[string]any{
		"type": "string", "minLength": 1, "maxLength": 4096,
		"description": "Workspace-relative destination path ending in .png.",
	}
	overwrite := map[string]any{
		"type": "boolean", "default": false,
		"description": "Allow replacing an existing screenshot only when expected_sha256 matches its current content.",
	}
	expected := map[string]any{
		"type": "string", "pattern": "^[0-9a-f]{64}$",
		"description": "Observed SHA-256 of the existing destination; required only when overwrite=true and rejected for create-only saves.",
	}
	tool.Description = "Capture and atomically save a bounded PNG screenshot inside the workspace. Create-only saves reject overwrite guards; overwrite=true requires expected_sha256 and preserves the previous revision."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "browser_save_screenshotArguments", "additionalProperties": false,
		"properties": map[string]any{
			"path": path, "full_page": fullPage, "overwrite": overwrite, "expected_sha256": expected,
		},
		"required": []string{"path"},
		"oneOf": []any{
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"path": path, "full_page": fullPage,
					"overwrite": map[string]any{"type": "boolean", "const": false},
				},
				"required": []string{"path"},
			},
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"path": path, "full_page": fullPage,
					"overwrite":       map[string]any{"type": "boolean", "const": true},
					"expected_sha256": expected,
				},
				"required": []string{"path", "overwrite", "expected_sha256"},
			},
		},
	}
	tool.OutputSchema = browserSaveScreenshotOutputSchema()
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = false
		tool.Annotations.DestructiveHint = boolPointer(true)
		tool.Annotations.IdempotentHint = false
		tool.Annotations.OpenWorldHint = boolPointer(true)
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"save": {
			Replay: ReplayUnsafe, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "workspace_read action=file",
		},
	})
}

func browserShareScreenshotOutputSchema() map[string]any {
	base := browserScreenshotMetadataSchema()
	properties := base["properties"].(map[string]any)
	properties["share_id"] = map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{43}$"}
	properties["url"] = map[string]any{"type": "string"}
	properties["expires_at"] = map[string]any{"type": "string"}
	properties["display_markdown"] = map[string]any{"type": "string"}
	required := append([]string(nil), base["required"].([]string)...)
	base["required"] = append(required, "share_id", "url", "expires_at", "display_markdown")
	return base
}

func overrideBrowserShareScreenshot(tool *mcp.Tool) error {
	properties := browserScreenshotBaseInput()
	properties["request_id"] = RequestIDSchema("Caller-owned UUID used to replay one screenshot share without creating duplicate temporary links.")
	properties["ttl_seconds"] = map[string]any{
		"type": "integer", "minimum": 60, "maximum": 3600, "default": 900,
		"description": "Temporary image-link lifetime in seconds; request-ID replay is retained for this lifetime.",
	}
	tool.Description = "Capture the active browser tab and publish one temporary image link. request_id makes retries replay the original share identity instead of creating duplicate links."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "browser_share_screenshotArguments", "additionalProperties": false,
		"properties": properties, "required": []string{"request_id"},
	}
	tool.OutputSchema = browserShareScreenshotOutputSchema()
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
