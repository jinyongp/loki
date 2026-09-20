package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func coordinationUUIDSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"}
}

func coordinationEntitySchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties": map[string]any{
			"id":            map[string]any{"type": "string", "minLength": 1},
			"kind":          map[string]any{"type": "string", "minLength": 1},
			"task_id":       coordinationUUIDSchema(),
			"run_id":        coordinationUUIDSchema(),
			"workstream_id": coordinationUUIDSchema(),
			"directory":     map[string]any{"type": "string"},
			"running":       map[string]any{"type": "boolean"},
			"status":        map[string]any{"type": "string"},
			"state":         map[string]any{"type": "string"},
			"allowed_actions": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
			},
			"current_run": map[string]any{
				"anyOf": []any{
					map[string]any{"type": "object", "additionalProperties": true},
					map[string]any{"type": "null"},
				},
			},
		},
	}
}

func coordinationProfileRevisionProperties() map[string]any {
	return map[string]any{
		"profile": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 128,
			"pattern": "^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$",
		},
		"revision": map[string]any{"type": "integer", "minimum": 0},
	}
}

func coordinationNextOutputSchema() map[string]any {
	properties := coordinationProfileRevisionProperties()
	properties["item"] = map[string]any{"anyOf": []any{coordinationEntitySchema(), map[string]any{"type": "null"}}}
	properties["reason"] = map[string]any{"type": "string"}
	properties["truncated"] = map[string]any{"type": "boolean"}
	properties["complete"] = map[string]any{"type": "boolean"}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties,
		"required":   []string{"profile", "revision", "item", "reason", "truncated", "complete"},
	}
}

func coordinationShowOutputSchema() map[string]any {
	properties := coordinationProfileRevisionProperties()
	properties["item"] = coordinationEntitySchema()
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties,
		"required":   []string{"profile", "revision", "item"},
	}
}

func coordinationPageOutputSchema() map[string]any {
	properties := coordinationProfileRevisionProperties()
	properties["items"] = map[string]any{
		"type": "array", "maxItems": 200, "items": coordinationEntitySchema(),
	}
	properties["next_cursor"] = map[string]any{
		"anyOf": []any{map[string]any{"type": "string", "maxLength": 4096}, map[string]any{"type": "null"}},
	}
	properties["truncated"] = map[string]any{"type": "boolean"}
	properties["complete"] = map[string]any{"type": "boolean"}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties,
		"required":   []string{"profile", "revision", "items", "next_cursor", "truncated", "complete"},
	}
}

func coordinationContextOutputSchema() map[string]any {
	properties := coordinationProfileRevisionProperties()
	properties["item"] = coordinationEntitySchema()
	properties["documents"] = map[string]any{
		"anyOf": []any{
			map[string]any{"type": "object", "additionalProperties": true},
			map[string]any{"type": "null"},
		},
	}
	for _, name := range []string{"tasks", "validations", "history"} {
		properties[name] = map[string]any{
			"type": "array", "items": coordinationEntitySchema(),
		}
	}
	properties["truncated"] = map[string]any{"type": "boolean"}
	properties["omitted_ids"] = map[string]any{
		"type": "array", "items": map[string]any{"type": "string", "minLength": 1},
	}
	properties["complete"] = map[string]any{"type": "boolean"}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties,
		"required": []string{
			"profile", "revision", "item", "documents", "tasks", "validations",
			"history", "truncated", "omitted_ids", "complete",
		},
	}
}

func overrideProjectCoordination(tool *mcp.Tool) error {
	cwd := map[string]any{
		"type": "string", "minLength": 1, "maxLength": 4096, "default": ".",
		"description": "Workspace-relative project directory used to resolve the canonical devtools profile.",
	}
	taskID := coordinationUUIDSchema()
	taskID["description"] = "Canonical task UUID used by task-specific reads."
	runID := coordinationUUIDSchema()
	runID["description"] = "Canonical Run UUID used by checkpoint_list."
	workstreamID := coordinationUUIDSchema()
	workstreamID["description"] = "Canonical workstream UUID used by workstream-specific reads or as an optional next-task filter."
	cursor := map[string]any{
		"type": "string", "maxLength": 4096,
		"description": "Opaque continuation cursor returned by the same paginated coordination action.",
	}
	limit := map[string]any{
		"type": "integer", "minimum": 1, "maximum": 200,
		"description": "Maximum number of canonical items to return for paginated actions.",
	}
	input, err := (ActionInputContract{
		Title:             "project_coordinationArguments",
		ActionDescription: "Canonical devtools coordination read to perform.",
		Fields: []ActionField{
			{Name: "cwd", Schema: cwd},
			{Name: "task_id", Schema: taskID},
			{Name: "run_id", Schema: runID},
			{Name: "workstream_id", Schema: workstreamID},
			{Name: "cursor", Schema: cursor},
			{Name: "limit", Schema: limit},
		},
		Variants: []ActionVariant{
			{Name: "next", Optional: []string{"cwd", "workstream_id"}},
			{Name: "task_show", Required: []string{"task_id"}, Optional: []string{"cwd"}},
			{Name: "current", Optional: []string{"cwd", "cursor", "limit"}},
			{Name: "task_context", Required: []string{"task_id"}, Optional: []string{"cwd"}},
			{Name: "task_history", Required: []string{"task_id"}, Optional: []string{"cwd", "cursor", "limit"}},
			{Name: "checkpoint_list", Required: []string{"run_id"}, Optional: []string{"cwd", "cursor", "limit"}},
			{Name: "workstream_list", Optional: []string{"cwd", "cursor", "limit"}},
			{Name: "workstream_show", Required: []string{"workstream_id"}, Optional: []string{"cwd"}},
			{Name: "workstream_context", Required: []string{"workstream_id"}, Optional: []string{"cwd"}},
			{Name: "workstream_history", Required: []string{"workstream_id"}, Optional: []string{"cwd", "cursor", "limit"}},
		},
	}).Schema()
	if err != nil {
		return err
	}

	tool.Description = "Read canonical devtools-backed task, workstream, Run, context, history, and checkpoint state with action-specific identifiers and explicit pagination/completeness."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{
		"type": "object",
		"oneOf": []any{
			coordinationNextOutputSchema(),
			coordinationShowOutputSchema(),
			coordinationPageOutputSchema(),
			coordinationContextOutputSchema(),
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
