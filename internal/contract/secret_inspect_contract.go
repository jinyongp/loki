package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func secretProfileItemSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
			"secret_names": map[string]any{
				"type": "array", "maxItems": 512,
				"items": map[string]any{"type": "string", "pattern": "^[A-Za-z_][A-Za-z0-9_.-]{0,127}$"},
			},
			"secret_count":            map[string]any{"type": "integer", "minimum": 0, "maximum": 512},
			"configured_secret_count": map[string]any{"type": "integer", "minimum": 0, "maximum": 512},
			"empty_secret_names": map[string]any{
				"type": "array", "maxItems": 512,
				"items": map[string]any{"type": "string", "pattern": "^[A-Za-z_][A-Za-z0-9_.-]{0,127}$"},
			},
		},
		"required": []string{"name", "secret_names", "secret_count", "configured_secret_count", "empty_secret_names"},
	}
}

func paginationProperties(itemKey string, itemSchema map[string]any) map[string]any {
	return map[string]any{
		itemKey:    map[string]any{"type": "array", "items": itemSchema},
		"offset":   map[string]any{"type": "integer", "minimum": 0},
		"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		"has_more": map[string]any{"type": "boolean"},
		"next_offset": map[string]any{
			"anyOf": []any{map[string]any{"type": "integer", "minimum": 0}, map[string]any{"type": "null"}},
		},
		"total":    map[string]any{"type": "integer", "minimum": 0},
		"complete": map[string]any{"type": "boolean"},
	}
}

func paginatedOutputSchema(itemKey string, itemSchema map[string]any, extras map[string]any) map[string]any {
	properties := paginationProperties(itemKey, itemSchema)
	required := []string{itemKey, "offset", "limit", "has_more", "next_offset", "total", "complete"}
	for key, schema := range extras {
		properties[key] = schema
		required = append(required, key)
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties, "required": required,
	}
}

func secretImportItemSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"import_id":  map[string]any{"type": "string", "pattern": "^[a-f0-9]{32}$"},
			"bytes":      map[string]any{"type": "integer", "minimum": 1, "maximum": 2000000},
			"created_at": map[string]any{"type": "number", "minimum": 0},
		},
		"required": []string{"import_id", "bytes", "created_at"},
	}
}

func secretAuditRecordSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"timestamp": map[string]any{"type": "number", "minimum": 0},
			"operation": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"uid":       map[string]any{"type": "integer", "minimum": 0},
			"success":   map[string]any{"type": "boolean"},
			"profile": map[string]any{
				"anyOf": []any{
					map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
					map[string]any{"type": "null"},
				},
			},
		},
		"required": []string{"timestamp", "operation", "uid", "success", "profile"},
	}
}

func secretStatusOutputSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"initialized": map[string]any{"const": true},
			"profiles":    map[string]any{"type": "integer", "minimum": 0, "maximum": 128},
			"revision":    map[string]any{"type": "integer", "minimum": 1},
			"policy_generation": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"schema": map[string]any{"type": "integer", "minimum": 1},
					"sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
				},
				"required": []string{"schema", "sha256"},
			},
			"devtools": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"version":           map[string]any{"type": "string"},
					"commit":            map[string]any{"type": "string"},
					"protocol_version":  map[string]any{"type": "integer", "minimum": 0},
					"approved_commands": map[string]any{"type": "integer", "minimum": 0},
					"catalog_sha256": map[string]any{
						"type": "string", "pattern": "^(?:|[0-9a-f]{64})$",
					},
				},
				"required": []string{"version", "commit", "protocol_version", "approved_commands", "catalog_sha256"},
			},
			"github": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"configured":           map[string]any{"type": "boolean"},
					"installation_count":   map[string]any{"type": "integer", "minimum": 0},
					"target_count":         map[string]any{"type": "integer", "minimum": 0},
					"credential_source":    map[string]any{"type": "string", "enum": []string{"disabled", "vault", "file"}},
					"credential_available": map[string]any{"type": "boolean"},
				},
				"required": []string{"configured", "installation_count", "target_count", "credential_source", "credential_available"},
			},
		},
		"required": []string{"initialized", "profiles", "revision", "policy_generation", "devtools", "github"},
	}
}

func overrideSecretInspect(tool *mcp.Tool) error {
	profile := map[string]any{
		"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$",
		"description": "Application secret profile name. Managed platform profiles are not exposed.",
	}
	offset := map[string]any{
		"type": "integer", "minimum": 0, "maximum": 1000000,
		"description": "Zero-based offset into the selected bounded metadata view.",
	}
	limit := map[string]any{
		"type": "integer", "minimum": 1, "maximum": 128,
		"description": "Maximum metadata records to return. Omit it to use the service default of 50.",
	}
	input, err := (ActionInputContract{
		Title:             "secret_inspectArguments",
		ActionDescription: "Encrypted secret metadata view to inspect; secret values are never returned.",
		Fields: []ActionField{
			{Name: "profile", Schema: profile},
			{Name: "offset", Schema: offset},
			{Name: "limit", Schema: limit},
		},
		Variants: []ActionVariant{
			{Name: "profiles", Optional: []string{"offset", "limit"}},
			{Name: "imports", Optional: []string{"offset", "limit"}},
			{Name: "profile", Required: []string{"profile"}},
			{Name: "status"},
			{Name: "audit", Optional: []string{"offset", "limit"}},
		},
	}).Schema()
	if err != nil {
		return err
	}

	profileOutput := secretProfileItemSchema()
	profileProperties := profileOutput["properties"].(map[string]any)
	profileProperties["revision"] = map[string]any{"type": "integer", "minimum": 1}
	profileOutput["required"] = append(profileOutput["required"].([]string), "revision")

	tool.Description = "Inspect encrypted-secret metadata without returning secret values. profiles/profile/status expose the vault revision used by guarded write/delete operations; profiles/imports/audit are bounded paginated views."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{
		"type": "object",
		"oneOf": []any{
			paginatedOutputSchema("profiles", secretProfileItemSchema(), map[string]any{
				"revision": map[string]any{"type": "integer", "minimum": 1},
			}),
			paginatedOutputSchema("imports", secretImportItemSchema(), nil),
			profileOutput,
			secretStatusOutputSchema(),
			paginatedOutputSchema("records", secretAuditRecordSchema(), nil),
		},
	}
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = true
		tool.Annotations.DestructiveHint = boolPointer(false)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(false)
	}
	return nil
}
