package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func overrideRevokeShare(tool *mcp.Tool) error {
	tool.Description = "Idempotently revoke one preview or artifact share. Repeating the same valid revoke returns the same terminal revoked state even when the share is already absent or expired."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "revoke_shareArguments", "additionalProperties": false,
		"properties": map[string]any{
			"kind": map[string]any{
				"type": "string", "enum": []string{"preview", "artifact"},
				"description": "Share type to revoke.",
			},
			"share_id": map[string]any{
				"type":        "string",
				"description": "Share identifier returned by publication or shared_resources.",
			},
		},
		"required": []string{"kind", "share_id"},
		"oneOf": []any{
			map[string]any{
				"properties": map[string]any{
					"kind":     map[string]any{"const": "preview"},
					"share_id": map[string]any{"type": "string", "pattern": "^[0-9a-f]{16}$"},
				},
			},
			map[string]any{
				"properties": map[string]any{
					"kind":     map[string]any{"const": "artifact"},
					"share_id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{43}$"},
				},
			},
		},
	}
	tool.OutputSchema = map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"kind":     map[string]any{"const": "preview"},
					"share_id": map[string]any{"type": "string", "pattern": "^[0-9a-f]{16}$"},
					"revoked":  map[string]any{"const": true},
				},
				"required": []string{"kind", "share_id", "revoked"},
			},
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"kind":     map[string]any{"const": "artifact"},
					"share_id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{43}$"},
					"revoked":  map[string]any{"const": true},
				},
				"required": []string{"kind", "share_id", "revoked"},
			},
		},
	}
	if tool.Annotations != nil {
		tool.Annotations.IdempotentHint = true
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"revoke": {
			Replay: ReplayIdempotent, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryNone,
			AffectedResourceLimit: 1, RecoveryReference: "repeat the same revoke_share call",
		},
	})
}
