package contract

func systemPolicyGenerationSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"schema": map[string]any{"type": "integer", "minimum": 0},
			"sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
		},
		"required": []string{"schema", "sha256"},
	}
}

func systemToolCatalogSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"revision": map[string]any{"type": "string", "minLength": 1},
			"count":    map[string]any{"type": "integer", "minimum": 0},
			"tools": map[string]any{
				"type": "array", "items": map[string]any{"type": "string", "minLength": 1},
			},
			"sha256":      map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"client_sync": map[string]any{"type": "string", "minLength": 1},
		},
		"required": []string{"revision", "count", "tools", "sha256", "client_sync"},
	}
}

func systemServerOutputSchema() map[string]any {
	capabilities := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"text_files": map[string]any{"type": "boolean"},
			"images": map[string]any{
				"type": "array", "items": map[string]any{"type": "string", "enum": []string{"gif", "jpeg", "png", "webp"}},
			},
			"temporary_image_links":   map[string]any{"type": "boolean"},
			"temporary_file_links":    map[string]any{"type": "boolean"},
			"workspace_bundles":       map[string]any{"type": "boolean"},
			"developer_output_viewer": map[string]any{"type": "boolean"},
			"temporary_live_previews": map[string]any{"type": "boolean"},
			"git_checkpoints":         map[string]any{"type": "boolean"},
			"file_revisions":          map[string]any{"type": "boolean"},
			"git_partial_staging":     map[string]any{"type": "boolean"},
			"signed_git_commits":      map[string]any{"type": "boolean"},
			"secret_profiles":         map[string]any{"type": "boolean"},
			"devtools": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"direct_cli":          map[string]any{"type": "boolean"},
					"project_state":       map[string]any{"type": "boolean"},
					"task_queues":         map[string]any{"type": "boolean"},
					"configured_commands": map[string]any{"type": "boolean"},
					"managed_processes":   map[string]any{"type": "boolean"},
					"workspace_ports":     map[string]any{"type": "boolean"},
				},
				"required": []string{"direct_cli", "project_state", "task_queues", "configured_commands", "managed_processes", "workspace_ports"},
			},
			"secret_management": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"vault":                  map[string]any{"type": "string", "minLength": 1},
					"opaque_staged_imports":  map[string]any{"type": "boolean"},
					"profile_lifecycle":      map[string]any{"type": "boolean"},
					"direct_value_access":    map[string]any{"type": "boolean"},
					"brokered_process_start": map[string]any{"type": "boolean"},
				},
				"required": []string{"vault", "opaque_staged_imports", "profile_lifecycle", "direct_value_access", "brokered_process_start"},
			},
			"agent_skills": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"revision":  map[string]any{"type": "string", "minLength": 1},
					"installed": map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}},
				},
				"required": []string{"revision", "installed"},
			},
			"github": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"configured":     map[string]any{"type": "boolean"},
					"target_count":   map[string]any{"type": "integer", "minimum": 0},
					"authentication": map[string]any{"type": "string", "minLength": 1},
				},
				"required": []string{"configured", "target_count", "authentication"},
			},
			"github_https":       map[string]any{"type": "boolean"},
			"structured_browser": map[string]any{"type": "boolean"},
			"browser_devtools":   map[string]any{"type": "boolean"},
			"browser_tool_catalog": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"revision": map[string]any{"type": "string", "minLength": 1},
					"count":    map[string]any{"type": "integer", "minimum": 0},
					"tools":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}},
				},
				"required": []string{"revision", "count", "tools"},
			},
		},
		"required": []string{
			"text_files", "images", "temporary_image_links", "temporary_file_links", "workspace_bundles",
			"developer_output_viewer", "temporary_live_previews", "git_checkpoints", "file_revisions",
			"git_partial_staging", "signed_git_commits", "secret_profiles", "devtools", "secret_management",
			"agent_skills", "github", "github_https", "structured_browser", "browser_devtools", "browser_tool_catalog",
		},
	}
	limits := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"max_file_bytes":        map[string]any{"type": "integer", "minimum": 1},
			"max_write_bytes":       map[string]any{"type": "integer", "minimum": 1},
			"max_image_bytes":       map[string]any{"type": "integer", "minimum": 1},
			"max_shared_file_bytes": map[string]any{"type": "integer", "minimum": 1},
			"max_bundle_files":      map[string]any{"type": "integer", "minimum": 1},
		},
		"required": []string{"max_file_bytes", "max_write_bytes", "max_image_bytes", "max_shared_file_bytes", "max_bundle_files"},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"name":              map[string]any{"const": "loki"},
			"version":           map[string]any{"type": "string"},
			"schema_revision":   map[string]any{"type": "string", "minLength": 1},
			"mcp_sdk_version":   map[string]any{"type": "string", "minLength": 1},
			"python_version":    map[string]any{"type": "null"},
			"go_version":        map[string]any{"type": "string", "minLength": 1},
			"uptime_seconds":    map[string]any{"type": "number", "minimum": 0},
			"server_time":       map[string]any{"type": "string", "minLength": 1},
			"workspace":         map[string]any{"const": "/workspace"},
			"policy_generation": systemPolicyGenerationSchema(),
			"tool_catalog":      systemToolCatalogSchema(),
			"capabilities":      capabilities,
			"limits":            limits,
		},
		"required": []string{
			"name", "version", "schema_revision", "mcp_sdk_version", "python_version", "go_version",
			"uptime_seconds", "server_time", "workspace", "policy_generation", "tool_catalog", "capabilities", "limits",
		},
	}
}

func systemWorkspaceOutputSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"root":       map[string]any{"const": "/workspace"},
			"repository": map[string]any{"type": "boolean"},
			"branch":     map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
			"limits": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"max_file_bytes":  map[string]any{"type": "integer", "minimum": 1},
					"max_write_bytes": map[string]any{"type": "integer", "minimum": 1},
					"max_patch_bytes": map[string]any{"type": "integer", "minimum": 1},
					"max_patch_files": map[string]any{"type": "integer", "minimum": 1},
				},
				"required": []string{"max_file_bytes", "max_write_bytes", "max_patch_bytes", "max_patch_files"},
			},
		},
		"required": []string{"root", "repository", "branch", "limits"},
	}
}

func systemDiagnosticsOutputSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"healthy": map[string]any{"type": "boolean"},
			"workspace": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"readable": map[string]any{"type": "boolean"}, "writable": map[string]any{"type": "boolean"}},
				"required":   []string{"readable", "writable"},
			},
			"audit_log": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"directory_writable": map[string]any{"type": "boolean"}},
				"required":   []string{"directory_writable"},
			},
			"github": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"configured":   map[string]any{"type": "boolean"},
					"target_count": map[string]any{"type": "integer", "minimum": 0},
					"protocol":     map[string]any{"const": "https"},
				},
				"required": []string{"configured", "target_count", "protocol"},
			},
			"git_signing": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"identity_configured":     map[string]any{"type": "boolean"},
					"format":                  map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
					"commit_signing_required": map[string]any{"type": "boolean"},
					"public_key_available":    map[string]any{"type": "boolean"},
					"agent_socket_available":  map[string]any{"type": "boolean"},
				},
				"required": []string{"identity_configured", "format", "commit_signing_required", "public_key_available", "agent_socket_available"},
			},
			"repositories": map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}},
			"tool_catalog": systemToolCatalogSchema(),
			"browser": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"socket_available":    map[string]any{"type": "boolean"},
					"catalog_revision":    map[string]any{"type": "string", "minLength": 1},
					"expected_tool_count": map[string]any{"type": "integer", "minimum": 0},
					"expected_tools":      map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}},
				},
				"required": []string{"socket_available", "catalog_revision", "expected_tool_count", "expected_tools"},
			},
		},
		"required": []string{"healthy", "workspace", "audit_log", "github", "git_signing", "repositories", "tool_catalog", "browser"},
	}
}

func toolActivityItemSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"invocation_id": map[string]any{"type": "string", "minLength": 1},
			"tool":          map[string]any{"type": "string", "minLength": 1},
			"state":         map[string]any{"type": "string", "enum": []string{"started", "terminal"}},
			"started_at":    map[string]any{"type": "string"},
			"terminal_at":   map[string]any{"type": "string"},
			"success":       map[string]any{"type": "boolean"},
			"outcome":       map[string]any{"type": "string"},
			"duration_ms":   map[string]any{"type": "number", "minimum": 0},
			"age_seconds":   map[string]any{"type": "number", "minimum": 0},
			"metadata":      map[string]any{"type": "object", "additionalProperties": true},
			"session_ref":   map[string]any{"type": "string"},
		},
		"required": []string{"invocation_id", "tool", "state"},
	}
}

func systemActivityOutputSchema(single bool) map[string]any {
	properties := map[string]any{
		"server_time": map[string]any{"type": "string", "minLength": 1},
	}
	required := []string{"server_time"}
	if single {
		properties["item"] = toolActivityItemSchema()
		required = append(required, "item")
	} else {
		properties["items"] = map[string]any{"type": "array", "maxItems": 50, "items": toolActivityItemSchema()}
		required = append(required, "items")
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties, "required": required,
	}
}

func systemPortOutputSchema() map[string]any {
	listener := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"pid":           map[string]any{"type": "integer", "minimum": 1},
			"cwd":           map[string]any{"type": "string"},
			"command":       map[string]any{"type": "string"},
			"local_address": map[string]any{"type": "string", "minLength": 1},
		},
		"required": []string{"pid", "cwd", "command", "local_address"},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"port":      map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
			"in_use":    map[string]any{"type": "boolean"},
			"listeners": map[string]any{"type": "array", "items": listener},
		},
		"required": []string{"port", "in_use", "listeners"},
	}
}

func systemInspectOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"oneOf": []any{
			systemServerOutputSchema(),
			systemWorkspaceOutputSchema(),
			systemDiagnosticsOutputSchema(),
			systemActivityOutputSchema(false),
			systemActivityOutputSchema(true),
			systemPortOutputSchema(),
		},
	}
}
