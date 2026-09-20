package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func contextDigestSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
}

func contextStringArray(maxItems int, maxLength int) map[string]any {
	item := map[string]any{"type": "string"}
	if maxLength > 0 {
		item["maxLength"] = maxLength
	}
	return map[string]any{"type": "array", "maxItems": maxItems, "items": item}
}

func contextSkillRevisionSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"name":     map[string]any{"type": "string", "pattern": "^[a-z0-9]+(?:-[a-z0-9]+)*$"},
			"scope":    map[string]any{"type": "string", "enum": []string{"project", "user", "packaged"}},
			"revision": contextDigestSchema(),
		},
		"required": []string{"name", "scope", "revision"},
	}
}

func contextBasisSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"repository_id":            contextDigestSchema(),
			"worktree_id":              contextDigestSchema(),
			"workstream_id":            coordinationUUIDSchema(),
			"task_id":                  coordinationUUIDSchema(),
			"run_id":                   coordinationUUIDSchema(),
			"coordination_fingerprint": contextDigestSchema(),
			"coordination_revision":    map[string]any{"type": "string", "maxLength": 1024},
			"history_cursor":           map[string]any{"type": "string", "maxLength": 4096},
			"code_basis":               contextDigestSchema(),
			"guidance_revision":        contextDigestSchema(),
			"skills": map[string]any{
				"type": "array", "maxItems": 256, "items": contextSkillRevisionSchema(),
			},
			"validation_record_ids": map[string]any{
				"type": "array", "maxItems": 200, "items": coordinationUUIDSchema(),
			},
			"evidence_refs": map[string]any{
				"type": "array", "maxItems": 200,
				"items": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024},
			},
			"gaps": map[string]any{
				"type": "array", "maxItems": 64,
				"items": map[string]any{
					"type": "string", "maxLength": 128,
					"pattern": "^[a-z0-9]+(?:[._-][a-z0-9]+)*$",
				},
			},
		},
		"required": []string{
			"repository_id", "worktree_id", "skills", "validation_record_ids", "evidence_refs", "gaps",
		},
	}
}

func contextRecordSchema() map[string]any {
	narrative := func() map[string]any {
		return map[string]any{
			"type": "array", "maxItems": 100,
			"items": map[string]any{"type": "string", "minLength": 1, "maxLength": 16384},
		}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"version":           map[string]any{"type": "integer", "minimum": 1},
			"id":                contextDigestSchema(),
			"request_id":        map[string]any{"type": "string", "pattern": RequestIDPattern},
			"created_at":        map[string]any{"type": "string", "minLength": 1},
			"scope_key":         contextDigestSchema(),
			"basis":             contextBasisSchema(),
			"basis_fingerprint": contextDigestSchema(),
			"draft_fingerprint": contextDigestSchema(),
			"summary":           map[string]any{"type": "string", "minLength": 1, "maxLength": 16384},
			"decisions":         narrative(),
			"remaining":         narrative(),
			"blockers":          narrative(),
			"next_action":       map[string]any{"type": "string", "minLength": 1, "maxLength": 16384},
			"session_ref":       map[string]any{"type": "string", "maxLength": 512},
		},
		"required": []string{
			"version", "id", "request_id", "created_at", "scope_key", "basis",
			"basis_fingerprint", "draft_fingerprint", "summary", "decisions",
			"remaining", "blockers", "next_action",
		},
	}
}

func contextRetentionGapSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"pruned_records":        map[string]any{"type": "integer", "minimum": 0},
			"last_pruned_at":        map[string]any{"type": "string", "minLength": 1},
			"last_pruned_record_id": contextDigestSchema(),
		},
		"required": []string{"pruned_records"},
	}
}

func contextCheckpointSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"found":         map[string]any{"type": "boolean"},
			"stale":         map[string]any{"type": "boolean"},
			"stale_reasons": contextStringArray(64, 128),
			"expected_previous": map[string]any{
				"anyOf": []any{map[string]any{"const": "missing"}, contextDigestSchema()},
			},
			"retention_gap": contextRetentionGapSchema(),
			"record":        contextRecordSchema(),
		},
		"required": []string{"found", "stale", "stale_reasons", "expected_previous"},
	}
}

func projectContextTransitionSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"action":          map[string]any{"type": "string", "enum": []string{"inspect", "claim", "none"}},
			"task_id":         coordinationUUIDSchema(),
			"run_id":          coordinationUUIDSchema(),
			"expected_run_id": coordinationUUIDSchema(),
			"reason":          map[string]any{"type": "string", "minLength": 1},
		},
		"required": []string{"action", "reason"},
	}
}

func projectContextProjectionSchema() map[string]any {
	entity := map[string]any{"type": "object", "additionalProperties": true}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"profile":   map[string]any{"type": "string"},
			"revision":  map[string]any{"type": "integer", "minimum": 0},
			"truncated": map[string]any{"type": "boolean"},
			"reason":    map[string]any{"type": "string"},
			"omitted_ids": map[string]any{
				"type": "array", "items": map[string]any{"type": "string", "minLength": 1},
			},
			"item":        entity,
			"items":       map[string]any{"type": "array", "items": entity},
			"next_cursor": map[string]any{"type": "string", "maxLength": 4096},
			"documents":   entity,
			"tasks":       map[string]any{"type": "array", "items": entity},
			"validations": map[string]any{"type": "array", "items": entity},
			"history":     map[string]any{"type": "array", "items": entity},
		},
		"required": []string{"profile", "revision", "truncated", "reason", "omitted_ids"},
	}
}

func projectContextCoordinationSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"current":      projectContextProjectionSchema(),
			"task_context": projectContextProjectionSchema(),
			"next":         projectContextProjectionSchema(),
			"transition":   projectContextTransitionSchema(),
		},
		"required": []string{"current", "transition"},
	}
}

func overrideProjectContext(tool *mcp.Tool) error {
	cwd := map[string]any{
		"type": "string", "default": ".", "minLength": 1, "maxLength": 4096,
		"description": "Workspace-relative starting directory used to resolve repository, Git, guidance, and coordination evidence.",
	}
	target := map[string]any{
		"type": "string", "default": ".", "minLength": 1, "maxLength": 4096,
		"description": "Path relative to cwd whose owning repository and guidance scope should be resumed.",
	}
	taskID := coordinationUUIDSchema()
	taskID["description"] = "Optional task UUID used only to disambiguate current or next coordination state."
	workstreamID := coordinationUUIDSchema()
	workstreamID["description"] = "Optional workstream UUID used to constrain next-task resolution."
	skills := map[string]any{
		"type": "array", "maxItems": 64, "uniqueItems": true,
		"items":       map[string]any{"type": "string", "pattern": "^[a-z0-9]+(?:-[a-z0-9]+)*$"},
		"description": "Selected effective Skill names whose current revisions become part of the context basis.",
	}
	tool.Description = "Compose bounded resumable project context from current guidance, selected Skill revisions, Git/worktree evidence, canonical coordination state, and the latest semantic checkpoint."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "project_contextArguments", "additionalProperties": false,
		"properties": map[string]any{
			"cwd": cwd, "target": target, "task_id": taskID, "workstream_id": workstreamID, "skills": skills,
		},
	}
	tool.OutputSchema = map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"complete":          map[string]any{"type": "boolean"},
			"basis":             contextBasisSchema(),
			"basis_fingerprint": contextDigestSchema(),
			"guidance":          guidanceResultSchema(),
			"skills":            skillCatalogSchema(),
			"selected_skills": map[string]any{
				"type": "array", "maxItems": 64,
				"items": map[string]any{"type": "string", "pattern": "^[a-z0-9]+(?:-[a-z0-9]+)*$"},
			},
			"coordination": projectContextCoordinationSchema(),
			"transition":   projectContextTransitionSchema(),
			"ambiguous":    map[string]any{"type": "boolean"},
			"checkpoint":   contextCheckpointSchema(),
		},
		"required": []string{
			"complete", "basis", "basis_fingerprint", "guidance", "skills",
			"selected_skills", "coordination", "transition", "ambiguous", "checkpoint",
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

func overrideProjectContextWrite(tool *mcp.Tool) error {
	cwd := map[string]any{
		"type": "string", "default": ".", "minLength": 1, "maxLength": 4096,
		"description": "Workspace-relative starting directory used to recompute the authoritative context basis.",
	}
	target := map[string]any{
		"type": "string", "default": ".", "minLength": 1, "maxLength": 4096,
		"description": "Path relative to cwd used to recompute repository, guidance, Git, and coordination evidence.",
	}
	taskID := coordinationUUIDSchema()
	taskID["description"] = "Optional task UUID used only to disambiguate the work identity before writing the checkpoint."
	workstreamID := coordinationUUIDSchema()
	workstreamID["description"] = "Optional workstream UUID used to constrain the recomputed work identity."
	skills := map[string]any{
		"type": "array", "maxItems": 64, "uniqueItems": true,
		"items":       map[string]any{"type": "string", "pattern": "^[a-z0-9]+(?:-[a-z0-9]+)*$"},
		"description": "Selected effective Skill names whose current revisions must match the checkpoint basis.",
	}
	requestID := RequestIDSchema("Caller-owned UUID making the context checkpoint write replay-safe.")
	expectedBasis := map[string]any{
		"type": "string", "pattern": "^[0-9a-f]{64}$",
		"description": "Basis fingerprint returned by the immediately preceding project_context read; changed evidence causes a conflict.",
	}
	expectedPrevious := map[string]any{
		"anyOf":       []any{map[string]any{"const": "missing"}, contextDigestSchema()},
		"description": "Latest checkpoint record ID returned by project_context, or missing when no checkpoint exists.",
	}
	summary := map[string]any{
		"type": "string", "minLength": 1, "maxLength": 16384,
		"description": "Bounded semantic summary of completed work and current state.",
	}
	narrative := func(description string) map[string]any {
		return map[string]any{
			"type": "array", "maxItems": 100,
			"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 16384},
			"description": description,
		}
	}
	nextAction := map[string]any{
		"type": "string", "minLength": 1, "maxLength": 16384,
		"description": "Bounded next concrete action for resuming the workstream.",
	}
	tool.Description = "Persist one bounded semantic handoff checkpoint after recomputing the authoritative project-context basis and verifying basis, previous-record, session-ownership, and request-ID replay preconditions."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "project_context_writeArguments", "additionalProperties": false,
		"properties": map[string]any{
			"cwd": cwd, "target": target, "task_id": taskID, "workstream_id": workstreamID,
			"skills": skills, "request_id": requestID, "expected_basis": expectedBasis,
			"expected_previous": expectedPrevious, "summary": summary,
			"decisions":   narrative("Bounded durable decisions that should survive handoff."),
			"remaining":   narrative("Bounded remaining work items that still need implementation or validation."),
			"next_action": nextAction,
			"blockers":    narrative("Bounded blockers that prevent or constrain the next action."),
		},
		"required": []string{"request_id", "expected_basis", "expected_previous", "summary", "next_action"},
	}
	tool.OutputSchema = map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"basis_fingerprint": contextDigestSchema(),
			"record":            contextRecordSchema(),
			"replayed":          map[string]any{"type": "boolean"},
			"pruned_records":    map[string]any{"type": "integer", "minimum": 0},
			"retention_gap": map[string]any{
				"anyOf": []any{contextRetentionGapSchema(), map[string]any{"type": "null"}},
			},
		},
		"required": []string{"basis_fingerprint", "record", "replayed", "pruned_records", "retention_gap"},
	}
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = false
		tool.Annotations.DestructiveHint = boolPointer(false)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(false)
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"checkpoint": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			ConcurrencyFields: []string{"expected_basis", "expected_previous"},
			FailureAtomicity:  FailureSingleResource, CrashRecovery: CrashRecoveryJournaled,
			AffectedResourceLimit: 1, RecoveryReference: "project_context",
		},
	})
}
