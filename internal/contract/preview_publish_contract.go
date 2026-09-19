package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func previewPublishResultSchema() map[string]any {
	schema := previewShareSchema()
	properties := schema["properties"].(map[string]any)
	properties["request_id"] = map[string]any{"type": "string", "pattern": RequestIDPattern}
	required := append([]string(nil), schema["required"].([]string)...)
	schema["required"] = append(required, "request_id")
	return schema
}

func overridePreviewPublish(tool *mcp.Tool) error {
	input, err := (ActionInputContract{
		Title:             "preview_publishArguments",
		ActionDescription: "Preview publication mode.",
		Fields: []ActionField{
			{Name: "request_id", Schema: RequestIDSchema("Caller-owned UUID used to replay one preview publication without creating duplicate shares during the share lifetime.")},
			{Name: "port", Schema: map[string]any{
				"type": "integer", "minimum": 1, "maximum": 65535,
				"description": "Runner-owned workspace development-server port for action=server.",
			}},
			{Name: "routes", Schema: map[string]any{
				"type": "object", "minProperties": 1, "maxProperties": 8,
				"additionalProperties": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
				"description":          "Public route-prefix to runner-owned workspace port map for action=stack. A root '/' route is required by the preview service.",
			}},
			{Name: "ttl_seconds", Schema: map[string]any{
				"type": "integer", "minimum": 60, "maximum": 86400, "default": 900,
				"description": "Preview lifetime in seconds. Request-ID replay is retained through this share lifetime.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "server", Required: []string{"request_id", "port"}, Optional: []string{"ttl_seconds"}},
			{Name: "stack", Required: []string{"request_id", "routes"}, Optional: []string{"ttl_seconds"}},
		},
	}).Schema()
	if err != nil {
		return err
	}
	tool.Description = "Publish one temporary live preview for a server or explicit route stack. Every creation requires a request_id, retries with the same inputs replay the original share while it is retained, and changed inputs with the same request_id conflict."
	tool.InputSchema = input
	tool.OutputSchema = previewPublishResultSchema()
	if tool.Annotations != nil {
		tool.Annotations.IdempotentHint = true
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"server": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryNone,
			AffectedResourceLimit: 1, RecoveryReference: "replay the same request_id while retained or inspect shared_resources kind=previews",
		},
		"stack": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryNone,
			AffectedResourceLimit: 1, RecoveryReference: "replay the same request_id while retained or inspect shared_resources kind=previews",
		},
	})
}
