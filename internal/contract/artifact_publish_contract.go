package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func artifactPublicationBaseProperties() map[string]any {
	return map[string]any{
		"kind":        map[string]any{"type": "string", "enum": []string{"file", "bundle"}},
		"share_id":    map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{43}$"},
		"url":         map[string]any{"type": "string"},
		"expires_at":  map[string]any{"type": "string"},
		"filename":    map[string]any{"type": "string", "minLength": 1},
		"mime_type":   map[string]any{"type": "string", "minLength": 1},
		"bytes":       map[string]any{"type": "integer", "minimum": 0, "maximum": 33554432},
		"sha256":      map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
		"disposition": map[string]any{"const": "attachment"},
	}
}

func artifactFileOutputSchema() map[string]any {
	properties := artifactPublicationBaseProperties()
	properties["kind"] = map[string]any{"const": "file"}
	properties["path"] = map[string]any{"type": "string", "minLength": 1}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties,
		"required": []string{
			"kind", "path", "share_id", "url", "expires_at", "filename",
			"mime_type", "bytes", "sha256", "disposition",
		},
	}
}

func artifactBundleOutputSchema() map[string]any {
	properties := artifactPublicationBaseProperties()
	properties["kind"] = map[string]any{"const": "bundle"}
	properties["mime_type"] = map[string]any{"const": "application/zip"}
	properties["paths"] = map[string]any{
		"type": "array", "minItems": 1, "maxItems": 64,
		"items": map[string]any{"type": "string", "minLength": 1},
	}
	properties["file_count"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 512}
	properties["input_bytes"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 33554432}
	properties["excluded_entries"] = map[string]any{"type": "integer", "minimum": 0}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties,
		"required": []string{
			"kind", "paths", "share_id", "url", "expires_at", "filename",
			"mime_type", "bytes", "sha256", "disposition",
			"file_count", "input_bytes", "excluded_entries",
		},
	}
}

func overrideArtifactPublish(tool *mcp.Tool) error {
	requestID := RequestIDSchema("Caller-owned UUID used to replay one artifact publication without creating duplicate temporary links.")
	ttl := map[string]any{
		"type": "integer", "minimum": 60, "maximum": 3600, "default": 900,
		"description": "Temporary download-link lifetime in seconds; request-ID replay is retained for this lifetime.",
	}
	path := map[string]any{
		"type": "string", "minLength": 1, "maxLength": 4096,
		"description": "Workspace-relative regular file path for action=file.",
	}
	paths := map[string]any{
		"type": "array", "minItems": 1, "maxItems": 64, "uniqueItems": true,
		"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		"description": "Workspace-relative files or directories to include in action=bundle. Paths are normalized, de-duplicated, and sorted before publication.",
	}
	filename := map[string]any{
		"type": "string", "minLength": 5, "maxLength": 240,
		"pattern":     "^[^/\\\\]+[.][zZ][iI][pP]$",
		"description": "Plain .zip filename for action=bundle; directory separators are not allowed. Omit it to use loki-workspace.zip.",
	}

	input, err := (ActionInputContract{
		Title:             "artifact_publishArguments",
		ActionDescription: "Artifact publication mode.",
		Fields: []ActionField{
			{Name: "request_id", Schema: requestID},
			{Name: "path", Schema: path},
			{Name: "paths", Schema: paths},
			{Name: "filename", Schema: filename},
			{Name: "ttl_seconds", Schema: ttl},
		},
		Variants: []ActionVariant{
			{Name: "file", Required: []string{"request_id", "path"}, Optional: []string{"ttl_seconds"}},
			{Name: "bundle", Required: []string{"request_id", "paths"}, Optional: []string{"filename", "ttl_seconds"}},
		},
	}).Schema()
	if err != nil {
		return err
	}

	tool.Description = "Publish one workspace file or a bounded ZIP bundle as a temporary download. File and bundle inputs/results are discriminated, and request_id replays the original publication without rebuilding or duplicating the share."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{
		"type":  "object",
		"oneOf": []any{artifactFileOutputSchema(), artifactBundleOutputSchema()},
	}
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = false
		tool.Annotations.DestructiveHint = boolPointer(false)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(true)
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"file": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryNone,
			AffectedResourceLimit: 1, RecoveryReference: "replay the same request_id while retained or inspect shared_resources kind=artifacts",
		},
		"bundle": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryNone,
			AffectedResourceLimit: 1, RecoveryReference: "replay the same request_id while retained or inspect shared_resources kind=artifacts",
		},
	})
}
