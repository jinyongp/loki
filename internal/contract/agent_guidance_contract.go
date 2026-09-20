package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func guidanceDiagnosticSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"code":    map[string]any{"type": "string", "minLength": 1},
			"message": map[string]any{"type": "string", "minLength": 1},
		},
		"required": []string{"path", "code", "message"},
	}
}

func guidanceSourceSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":     map[string]any{"type": "string", "minLength": 1},
			"scope":    map[string]any{"type": "string", "minLength": 1},
			"revision": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"bytes":    map[string]any{"type": "integer", "minimum": 0, "maximum": 524288},
			"content":  map[string]any{"type": "string", "maxLength": 524288},
		},
		"required": []string{"path", "scope", "revision", "bytes", "content"},
	}
}

func guidanceResultSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"target":     map[string]any{"type": "string", "minLength": 1},
			"target_dir": map[string]any{"type": "string", "minLength": 1},
			"revision":   map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"complete":   map[string]any{"type": "boolean"},
			"sources": map[string]any{
				"type": "array", "maxItems": 32, "items": guidanceSourceSchema(),
			},
			"diagnostics": map[string]any{
				"type": "array", "maxItems": 128, "items": guidanceDiagnosticSchema(),
			},
			"total_bytes": map[string]any{"type": "integer", "minimum": 0, "maximum": 2097152},
		},
		"required": []string{"target", "target_dir", "revision", "complete", "sources", "diagnostics", "total_bytes"},
	}
}

func skillDiagnosticSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"scope":   map[string]any{"type": "string"},
			"code":    map[string]any{"type": "string", "minLength": 1},
			"message": map[string]any{"type": "string", "minLength": 1},
		},
		"required": []string{"scope", "code", "message"},
	}
}

func skillShadowSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"name":           map[string]any{"type": "string", "pattern": "^[a-z0-9]+(?:-[a-z0-9]+)*$"},
			"selected_scope": map[string]any{"type": "string", "enum": []string{"project", "user", "packaged"}},
			"shadowed_scope": map[string]any{"type": "string", "enum": []string{"project", "user", "packaged"}},
		},
		"required": []string{"name", "selected_scope", "shadowed_scope"},
	}
}

func skillSummaryProperties() map[string]any {
	return map[string]any{
		"name":          map[string]any{"type": "string", "pattern": "^[a-z0-9]+(?:-[a-z0-9]+)*$"},
		"description":   map[string]any{"type": "string", "minLength": 1},
		"scope":         map[string]any{"type": "string", "enum": []string{"project", "user", "packaged"}},
		"revision":      map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
		"license":       map[string]any{"type": "string"},
		"compatibility": map[string]any{"type": "string"},
		"metadata": map[string]any{
			"type": "object", "additionalProperties": map[string]any{"type": "string"},
		},
		"allowed_tools":  map[string]any{"type": "string"},
		"resource_count": map[string]any{"type": "integer", "minimum": 0, "maximum": 512},
		"total_bytes":    map[string]any{"type": "integer", "minimum": 0, "maximum": 16777216},
	}
}

func skillSummarySchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": skillSummaryProperties(),
		"required":   []string{"name", "description", "scope", "revision", "resource_count", "total_bytes"},
	}
}

func skillCatalogSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"complete": map[string]any{"type": "boolean"},
			"items": map[string]any{
				"type": "array", "maxItems": 256, "items": skillSummarySchema(),
			},
			"diagnostics": map[string]any{
				"type": "array", "items": skillDiagnosticSchema(),
			},
			"shadowed": map[string]any{
				"type": "array", "items": skillShadowSchema(),
			},
		},
		"required": []string{"complete", "items", "diagnostics", "shadowed"},
	}
}

func skillResourceSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":       map[string]any{"type": "string", "minLength": 1},
			"size":       map[string]any{"type": "integer", "minimum": 0, "maximum": 2097152},
			"sha256":     map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"executable": map[string]any{"type": "boolean"},
		},
		"required": []string{"path", "size", "sha256", "executable"},
	}
}

func skillDetailSchema() map[string]any {
	properties := skillSummaryProperties()
	properties["content"] = map[string]any{"type": "string", "maxLength": 524288}
	properties["resources"] = map[string]any{
		"type": "array", "maxItems": 512, "items": skillResourceSchema(),
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties,
		"required": []string{
			"name", "description", "scope", "revision", "resource_count", "total_bytes",
			"content", "resources",
		},
	}
}

func skillInspectionSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"item":        skillDetailSchema(),
			"diagnostics": map[string]any{"type": "array", "items": skillDiagnosticSchema()},
			"shadowed":    map[string]any{"type": "array", "items": skillShadowSchema()},
		},
		"required": []string{"item", "diagnostics", "shadowed"},
	}
}

func overrideAgentGuidance(tool *mcp.Tool) error {
	input, err := (ActionInputContract{
		Title:             "agent_guidanceArguments",
		ActionDescription: "Target-scoped guidance operation to perform.",
		Fields: []ActionField{
			{Name: "cwd", Schema: map[string]any{
				"type": "string", "minLength": 1, "maxLength": 4096, "default": ".",
				"description": "Workspace-relative working directory used to resolve the target and project-scoped Skills.",
			}},
			{Name: "target", Schema: map[string]any{
				"type": "string", "minLength": 1, "maxLength": 4096, "default": ".",
				"description": "Workspace-relative file or directory whose applicable AGENTS.md chain and project Skill scope should be resolved.",
			}},
			{Name: "name", Schema: map[string]any{
				"type": "string", "pattern": "^[a-z0-9]+(?:-[a-z0-9]+)*$",
				"description": "Effective portable Agent Skill name to inspect; valid only for action=skill.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "context", Optional: []string{"cwd", "target"}},
			{Name: "skill", Required: []string{"name"}, Optional: []string{"cwd", "target"}},
		},
	}).Schema()
	if err != nil {
		return err
	}

	contextOutput := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"guidance": guidanceResultSchema(),
			"skills":   skillCatalogSchema(),
		},
		"required": []string{"guidance", "skills"},
	}
	skillOutput := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"skill":    skillInspectionSchema(),
			"complete": map[string]any{"const": true},
		},
		"required": []string{"skill", "complete"},
	}

	tool.Description = "Resolve bounded target-scoped AGENTS.md guidance and portable Agent Skill provenance. context returns completeness-aware guidance plus a metadata-only Skill catalog; skill loads one selected Skill body/resources on demand."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{"type": "object", "oneOf": []any{contextOutput, skillOutput}}
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = true
		tool.Annotations.DestructiveHint = boolPointer(false)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(false)
	}
	return nil
}
