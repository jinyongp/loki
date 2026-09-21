package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func jobStateSchema(description string) map[string]any {
	return map[string]any{
		"type": "string", "enum": []string{"admitted", "running", "terminal"},
		"description": description,
	}
}

func jobOutcomeSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"enum":        []string{"exited", "canceled", "timed_out", "oom_killed", "launch_failed", "outcome_unknown"},
		"description": description,
	}
}

func jobCleanupSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"enum":        []string{"pending", "complete", "failed", "not_required"},
		"description": description,
	}
}

func jobIDSchema(description string) map[string]any {
	return map[string]any{
		"type": "string", "pattern": "^[0-9a-f]{32}$",
		"description": description,
	}
}

func jobTimestampSchema(description string) map[string]any {
	return map[string]any{"type": "string", "format": "date-time", "description": description}
}

func jobNetworkSchema(description string) map[string]any {
	return map[string]any{
		"type": "string", "enum": []string{"none", "dependency-install"}, "default": "none",
		"description": description,
	}
}

func jobEndpointRequestSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"name": map[string]any{
				"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$",
				"description": "Logical endpoint name fixed for the Job lifetime.",
			},
			"port": map[string]any{
				"type": "integer", "minimum": 1024, "maximum": 65535,
				"description": "TCP port listened to by the workload inside its isolated Job network.",
			},
		},
		"required": []string{"name", "port"},
	}
}

func jobToolchainRefSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"family": map[string]any{
				"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$",
				"description": "Administrator-approved managed toolchain family selected from project declarations.",
			},
			"version": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 128,
				"description": "Canonical provider version selected for this Job.",
			},
			"generation_id": map[string]any{
				"type": "string", "pattern": "^[0-9a-f]{64}$",
				"description": "Immutable Loki-managed generation identity mounted read-only into the Job.",
			},
		},
		"required": []string{"family", "version", "generation_id"},
	}
}

func jobToolchainRefListSchema(description string) map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 8, "items": jobToolchainRefSchema(),
		"description": description,
	}
}

func jobEndpointLeaseSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"lease_id": map[string]any{
				"type": "string", "pattern": "^[0-9a-f]{32}$",
				"description": "Opaque lease identifier bound to this exact Job incarnation and endpoint.",
			},
			"job_id": jobIDSchema("Job that owns this endpoint lease."),
			"name": map[string]any{
				"type": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$",
				"description": "Logical endpoint name declared at Job start.",
			},
			"port": map[string]any{
				"type": "integer", "minimum": 1024, "maximum": 65535,
				"description": "TCP port listened to by the workload.",
			},
			"host_port": map[string]any{
				"type": "integer", "minimum": 1024, "maximum": 65535,
				"description": "Loopback-only ephemeral host port assigned to the trusted Job gateway.",
			},
			"state": map[string]any{
				"type": "string", "enum": []string{"active", "released"},
				"description": "Current durable endpoint lease state.",
			},
			"created_at": jobTimestampSchema("UTC timestamp when the endpoint lease was durably bound."),
			"updated_at": jobTimestampSchema("UTC timestamp of the latest endpoint lease transition."),
		},
		"required": []string{"lease_id", "job_id", "name", "port", "host_port", "state", "created_at", "updated_at"},
	}
}

func jobStatusOutputSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"job_id":      jobIDSchema("Opaque Loki Job identifier."),
			"state":       jobStateSchema("Current durable Job state."),
			"created_at":  jobTimestampSchema("UTC timestamp when the Job was durably admitted."),
			"updated_at":  jobTimestampSchema("UTC timestamp of the latest durable Job transition."),
			"deadline_at": jobTimestampSchema("UTC trusted execution deadline retained for the Job."),
			"network":     jobNetworkSchema("Logical network profile selected for this Job."),
			"endpoints": map[string]any{
				"type": "array", "maxItems": 8, "items": jobEndpointLeaseSchema(),
				"description": "Job-owned endpoint leases. Leases are absent until the exact gateway binding is durably established.",
			},
			"toolchains": jobToolchainRefListSchema("Immutable managed toolchain generations selected for this Job from project declarations."),
			"outcome":    jobOutcomeSchema("Terminal Job outcome when available."),
			"exit_code": map[string]any{
				"type": "integer", "minimum": 0, "maximum": 255,
				"description": "Observed process exit code when the backend produced one.",
			},
			"cleanup": jobCleanupSchema("Durable cleanup state when terminal evidence is available."),
			"truncated": map[string]any{
				"type":        "boolean",
				"description": "Whether retained Job output was truncated at Loki's configured bound.",
			},
		},
		"required": []string{"job_id", "state", "created_at", "updated_at", "deadline_at", "network", "endpoints", "toolchains"},
	}
}

func jobEndpointRequestListSchema(description string) map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 8, "items": jobEndpointRequestSchema(),
		"description": description,
	}
}

func jobTool() (*mcp.Tool, error) {
	input, err := (ActionInputContract{
		Title:             "jobArguments",
		ActionDescription: "Job lifecycle action to perform through Loki's isolated executor boundary.",
		Fields: []ActionField{
			{Name: "request_id", Schema: RequestIDSchema(
				"Caller-owned UUID that makes start replay-safe while the durable Job record is retained.",
			)},
			{Name: "cwd", Schema: map[string]any{
				"type": "string", "minLength": 1, "maxLength": 4096, "default": ".",
				"description": "Workspace-relative working directory inside the isolated execution workspace.",
			}},
			{Name: "argv", Schema: map[string]any{
				"type": "array", "minItems": 1, "maxItems": 256,
				"items":       map[string]any{"type": "string", "maxLength": 4096},
				"description": "Command argument vector. The first item is an absolute in-sandbox executable path; managed Node.js and pnpm commands use Loki-owned shims under /opt/loki/toolchain/bin.",
			}},
			{Name: "timeout_seconds", Schema: map[string]any{
				"type": "integer", "minimum": 1, "maximum": 86400,
				"description": "Requested Job lifetime in seconds; the trusted executor/launcher maximum may be lower.",
			}},
			{Name: "network", Schema: jobNetworkSchema(
				"Logical network profile. Use none for isolated execution or dependency-install for administrator-allowlisted HTTPS dependency access.",
			)},
			{Name: "endpoints", Schema: jobEndpointRequestListSchema(
				"Development endpoints declared before Job start. Each logical endpoint receives a loopback-only ephemeral lease after the trusted gateway starts.",
			)},
			{Name: "job_id", Schema: jobIDSchema(
				"Opaque Job identifier returned by start and used by inspect, output, or cancel.",
			)},
		},
		Variants: []ActionVariant{
			{Name: "start", Required: []string{"request_id", "argv"}, Optional: []string{"cwd", "timeout_seconds", "network", "endpoints"}},
			{Name: "inspect", Required: []string{"job_id"}},
			{Name: "output", Required: []string{"job_id"}},
			{Name: "cancel", Required: []string{"job_id"}},
		},
	}).Schema()
	if err != nil {
		return nil, err
	}

	startOutput := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string", "const": "start",
				"description": "Completed Job action.",
			},
			"request_id": RequestIDSchema("Normalized caller request UUID bound to this Job start."),
			"job_id":     jobIDSchema("Opaque deterministic Job identifier derived from request_id."),
			"state":      jobStateSchema("Current durable Job state at start/replay return time."),
			"replayed": map[string]any{
				"type":        "boolean",
				"description": "Whether this response reused the retained Job for the same request_id and normalized start input.",
			},
			"detached": map[string]any{
				"type": "boolean", "const": true,
				"description": "True because Job lifetime is independent from the MCP request connection.",
			},
			"deadline_at": jobTimestampSchema("UTC trusted execution deadline for the retained Job."),
			"network":     jobNetworkSchema("Normalized logical network profile bound to this start request."),
			"endpoint_requests": jobEndpointRequestListSchema(
				"Normalized logical endpoint declarations bound to this start request.",
			),
			"toolchains": jobToolchainRefListSchema(
				"Managed toolchain generations selected by Loki from project declarations for this start request.",
			),
		},
		"required": []string{
			"action", "request_id", "job_id", "state", "replayed", "detached", "deadline_at", "network", "endpoint_requests", "toolchains",
		},
	}

	inspectOutput := jobStatusOutputSchema()
	inspectOutput["properties"].(map[string]any)["action"] = map[string]any{
		"type": "string", "const": "inspect", "description": "Completed Job action.",
	}
	inspectOutput["required"] = append([]string{"action"}, inspectOutput["required"].([]string)...)

	outputOutput := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string", "const": "output",
				"description": "Completed Job action.",
			},
			"job_id": jobIDSchema("Opaque Job identifier."),
			"state":  jobStateSchema("Current durable Job state observed with this output snapshot."),
			"output": map[string]any{
				"type": "string", "maxLength": 4194304,
				"description": "Bounded combined stdout/stderr snapshot decoded as valid UTF-8.",
			},
			"truncated": map[string]any{
				"type":        "boolean",
				"description": "Whether the snapshot was truncated at Loki's configured output bound.",
			},
			"complete": map[string]any{
				"type":        "boolean",
				"description": "Whether the snapshot comes from a retained terminal Job result.",
			},
		},
		"required": []string{"action", "job_id", "state", "output", "truncated", "complete"},
	}

	cancelStatus := jobStatusOutputSchema()
	cancelOutput := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string", "const": "cancel",
				"description": "Completed Job action.",
			},
			"job_id": jobIDSchema("Opaque Job identifier targeted by cancellation."),
			"canceled": map[string]any{
				"type":        "boolean",
				"description": "True when this call caused an active Job cancellation; false when it was already terminal.",
			},
			"status": cancelStatus,
		},
		"required": []string{"action", "job_id", "canceled", "status"},
	}

	tool := &mcp.Tool{
		Name:        "job",
		Description: "Start replay-safe asynchronous Jobs through Loki's isolated executor boundary, inspect durable state, selected managed toolchains and Job-owned endpoint leases, read bounded output snapshots, or cancel one Job. Project toolchain declarations are resolved by Loki; public arguments cannot select generation IDs, host mounts, launcher, host ports, policy, image, credentials, Docker, or backend instance authority.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: boolPointer(true), IdempotentHint: false,
			OpenWorldHint: boolPointer(false), ReadOnlyHint: false,
		},
		InputSchema: input,
		OutputSchema: map[string]any{
			"type":  "object",
			"oneOf": []any{startOutput, inspectOutput, outputOutput, cancelOutput},
		},
	}
	if err := ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"start": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryJournaled,
			AffectedResourceLimit: 1, RecoveryReference: "job action=inspect with the returned job_id; retry the same request_id after an uncertain start",
		},
		"inspect": {
			Replay: ReplayIdempotent, FailureAtomicity: FailureNone, CrashRecovery: CrashRecoveryNone,
			AffectedResourceLimit: 1, RecoveryReference: "retry job action=inspect",
		},
		"output": {
			Replay: ReplayIdempotent, FailureAtomicity: FailureNone, CrashRecovery: CrashRecoveryNone,
			AffectedResourceLimit: 1, RecoveryReference: "retry job action=output or inspect the Job",
		},
		"cancel": {
			Replay: ReplayIdempotent, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "job action=inspect",
		},
	}); err != nil {
		return nil, err
	}
	return tool, nil
}
