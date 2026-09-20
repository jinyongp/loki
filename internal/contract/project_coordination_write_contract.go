package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func coordinationWriteBranch(action string, fields map[string]map[string]any, required []string) map[string]any {
	properties := map[string]any{
		"action": map[string]any{
			"type": "string", "const": action,
			"description": "Canonical project coordination transition to apply.",
		},
	}
	for name, schema := range fields {
		properties[name] = schema
	}
	needed := []string{"action", "request_id"}
	needed = append(needed, required...)
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties, "required": needed,
	}
}

func coordinationMutationOutputSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"claim", "takeover", "resume", "checkpoint", "release", "done"},
			},
			"request_id": coordinationUUIDSchema(),
			"profile": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 128,
				"pattern": "^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$",
			},
			"revision":          map[string]any{"type": "integer", "minimum": 0},
			"previous_revision": map[string]any{"type": "integer", "minimum": 0},
			"current_revision":  map[string]any{"type": "integer", "minimum": 0},
			"affected_count":    map[string]any{"type": "integer", "minimum": 0},
			"affected_ids": map[string]any{
				"type": "array", "items": map[string]any{"type": "string", "minLength": 1},
			},
			"replayed": map[string]any{"type": "boolean"},
			"changed":  map[string]any{"type": "boolean"},
			"action_ids": map[string]any{
				"type": "array", "items": map[string]any{"type": "string", "minLength": 1},
			},
			"item": map[string]any{
				"anyOf": []any{coordinationEntitySchema(), map[string]any{"type": "null"}},
			},
			"run": map[string]any{
				"anyOf": []any{coordinationEntitySchema(), map[string]any{"type": "null"}},
			},
			"claimed":   map[string]any{"type": "boolean"},
			"record_id": map[string]any{"type": "string", "minLength": 1},
			"details": map[string]any{
				"type": "object", "additionalProperties": true,
				"description": "Additional sanitized devtools mutation metadata not promoted into the stable coordination envelope.",
			},
		},
		"required": []string{"action", "request_id", "profile", "revision", "details"},
	}
}

func overrideProjectCoordinationWrite(tool *mcp.Tool) error {
	cwd := map[string]any{
		"type": "string", "minLength": 1, "maxLength": 4096, "default": ".",
		"description": "Workspace-relative project directory used to resolve the canonical devtools profile.",
	}
	requestID := RequestIDSchema("Caller-owned UUID making the canonical devtools transition replay-safe.")
	taskID := coordinationUUIDSchema()
	taskID["description"] = "Task UUID explicitly targeted by claim, takeover, resume, or done."
	runID := coordinationUUIDSchema()
	runID["description"] = "Run UUID explicitly targeted by checkpoint or release."
	workstreamID := coordinationUUIDSchema()
	workstreamID["description"] = "Workstream UUID used only to filter an automatic claim."
	expectedRunID := coordinationUUIDSchema()
	expectedRunID["description"] = "Observed current Run UUID required by takeover as a concurrency precondition."
	summary := map[string]any{
		"type": "string", "minLength": 1, "maxLength": 16384,
		"description": "Bounded progress or completion summary; required for checkpoint and done, optional for release.",
	}
	textList := func(description string) map[string]any {
		return map[string]any{
			"type": "array", "maxItems": 100,
			"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 16384},
			"description": description,
		}
	}
	decisions := textList("Bounded durable decisions to attach to checkpoint or release progress.")
	remaining := textList("Bounded remaining work descriptions to attach to checkpoint or release progress.")
	blockers := textList("Bounded blockers to attach to checkpoint or release progress.")
	validationIDs := map[string]any{
		"type": "array", "maxItems": 200, "uniqueItems": true,
		"items":       coordinationUUIDSchema(),
		"description": "Canonical validation record UUIDs to associate with checkpoint, release, or done.",
	}
	nextAction := map[string]any{
		"type": "string", "minLength": 1, "maxLength": 16384,
		"description": "Bounded next action to attach to checkpoint or release progress.",
	}

	rootProperties := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        []string{"claim", "takeover", "resume", "checkpoint", "release", "done"},
			"description": "Canonical project coordination transition to apply.",
		},
		"cwd": cwd, "request_id": requestID, "task_id": taskID, "run_id": runID,
		"workstream_id": workstreamID, "expected_run_id": expectedRunID,
		"summary": summary, "decisions": decisions, "validation_record_ids": validationIDs,
		"remaining": remaining, "next_action": nextAction, "blockers": blockers,
	}

	with := func(names ...string) map[string]map[string]any {
		fields := map[string]map[string]any{"request_id": requestID}
		for _, name := range names {
			fields[name] = rootProperties[name].(map[string]any)
		}
		return fields
	}
	branches := []any{
		coordinationWriteBranch("claim", with("cwd"), nil),
		coordinationWriteBranch("claim", with("cwd", "task_id"), []string{"task_id"}),
		coordinationWriteBranch("claim", with("cwd", "workstream_id"), []string{"workstream_id"}),
		coordinationWriteBranch("takeover", with("cwd", "task_id", "expected_run_id"), []string{"task_id", "expected_run_id"}),
		coordinationWriteBranch("resume", with("cwd", "task_id"), []string{"task_id"}),
		coordinationWriteBranch("checkpoint", with(
			"cwd", "run_id", "summary", "decisions", "validation_record_ids", "remaining", "next_action", "blockers",
		), []string{"run_id", "summary"}),
		coordinationWriteBranch("release", with(
			"cwd", "run_id", "summary", "decisions", "validation_record_ids", "remaining", "next_action", "blockers",
		), []string{"run_id"}),
		coordinationWriteBranch("done", with("cwd", "task_id", "summary", "validation_record_ids"), []string{"task_id", "summary"}),
	}

	tool.Description = "Apply session-bound canonical devtools claim, takeover, resume, checkpoint, release, or done transitions. Every transition is request-ID replay-safe; takeover additionally requires the observed Run ID. Private claim context remains server-owned."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "project_coordination_writeArguments", "additionalProperties": false,
		"properties": rootProperties, "required": []string{"action", "request_id"}, "oneOf": branches,
	}
	tool.OutputSchema = coordinationMutationOutputSchema()
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = false
		tool.Annotations.DestructiveHint = boolPointer(true)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(false)
	}

	upstream := func(reference string) OperationSemantics {
		return OperationSemantics{
			Replay: ReplayRequestID, RequestIDField: "request_id",
			FailureAtomicity: FailureUpstream, CrashRecovery: CrashRecoveryUpstream,
			AffectedResourceLimit: 1, RecoveryReference: reference,
		}
	}
	operations := map[string]OperationSemantics{
		"claim":      upstream("project_coordination action=current and action=next"),
		"takeover":   upstream("project_coordination action=current and action=task_context"),
		"resume":     upstream("project_coordination action=current and action=task_context"),
		"checkpoint": upstream("project_coordination action=checkpoint_list"),
		"release":    upstream("project_coordination action=current"),
		"done":       upstream("project_coordination action=task_show"),
	}
	takeover := operations["takeover"]
	takeover.ConcurrencyFields = []string{"expected_run_id"}
	operations["takeover"] = takeover
	return ApplyOperationMetadata(tool, operations)
}
