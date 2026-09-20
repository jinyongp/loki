package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func secretProfileFieldSchema() map[string]any {
	return map[string]any{
		"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$",
		"description": "Application secret profile name. Managed platform profiles are unavailable.",
	}
}

func secretNameFieldSchema() map[string]any {
	return map[string]any{
		"type": "string", "pattern": "^[A-Za-z_][A-Za-z0-9_.-]{0,127}$",
		"description": "Private secret key name within the application profile.",
	}
}

func publicConfigNameFieldSchema() map[string]any {
	return map[string]any{
		"type": "string", "pattern": "^[A-Za-z_][A-Za-z0-9_.-]{0,127}$",
		"description": "Non-sensitive public configuration key name within the application profile.",
	}
}

func secretExpectedRevisionSchema() map[string]any {
	return map[string]any{
		"type": "integer", "minimum": 1,
		"description": "Vault revision observed from secret_inspect profiles, profile, or status. Mutation fails with conflict if the revision changed.",
	}
}

func secretMutationBaseOutput(properties map[string]any, required ...string) map[string]any {
	base := map[string]any{
		"profile":  map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
		"revision": map[string]any{"type": "integer", "minimum": 2},
	}
	for key, schema := range properties {
		base[key] = schema
	}
	allRequired := append([]string{"profile", "revision"}, required...)
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": base, "required": allRequired,
	}
}

func overrideSecretWrite(tool *mcp.Tool) error {
	profile := secretProfileFieldSchema()
	expected := secretExpectedRevisionSchema()
	requestID := RequestIDSchema("Caller-owned UUID used to replay one vault-only secret mutation after a lost response.")
	secretName := secretNameFieldSchema()
	publicName := publicConfigNameFieldSchema()
	importID := map[string]any{
		"type": "string", "pattern": "^[a-f0-9]{32}$",
		"description": "Opaque staged dotenv import ID returned by secret_inspect action=imports.",
	}
	publicValue := map[string]any{
		"type": "string", "maxLength": 65536,
		"description": "Non-sensitive public configuration value. It is visible in the MCP request; do not use this action for credentials or private secret values.",
	}
	bytes := map[string]any{
		"type": "integer", "minimum": 16, "maximum": 128, "default": 32,
		"description": "Random-byte count used to generate a new secret before URL-safe base64 encoding.",
	}
	input, err := (ActionInputContract{
		Title:             "secret_writeArguments",
		ActionDescription: "Guarded application-secret mutation to perform.",
		Fields: []ActionField{
			{Name: "profile", Schema: profile},
			{Name: "expected_revision", Schema: expected},
			{Name: "request_id", Schema: requestID},
			{Name: "secret", Schema: secretName},
			{Name: "name", Schema: publicName},
			{Name: "import_id", Schema: importID},
			{Name: "value", Schema: publicValue},
			{Name: "bytes", Schema: bytes},
		},
		Variants: []ActionVariant{
			{Name: "create_profile", Required: []string{"profile", "expected_revision", "request_id"}},
			{Name: "import_staged", Required: []string{"profile", "expected_revision", "request_id", "import_id"}},
			{Name: "set_public", Required: []string{"profile", "expected_revision", "request_id", "name", "value"}},
			{Name: "generate", Required: []string{"profile", "expected_revision", "request_id", "secret"}, Optional: []string{"bytes"}},
		},
	}).Schema()
	if err != nil {
		return err
	}

	requestResult := map[string]any{"type": "string", "pattern": RequestIDPattern}
	createOutput := secretMutationBaseOutput(map[string]any{
		"created":    map[string]any{"const": true},
		"request_id": requestResult,
	}, "created", "request_id")
	setOutput := secretMutationBaseOutput(map[string]any{
		"name":       map[string]any{"type": "string"},
		"stored":     map[string]any{"const": true},
		"request_id": requestResult,
	}, "name", "stored", "request_id")
	generateOutput := secretMutationBaseOutput(map[string]any{
		"secret":     map[string]any{"type": "string"},
		"generated":  map[string]any{"const": true},
		"bytes":      map[string]any{"type": "integer", "minimum": 16, "maximum": 128},
		"request_id": requestResult,
	}, "secret", "generated", "bytes", "request_id")
	importOutput := secretMutationBaseOutput(map[string]any{
		"imported": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 512,
			"items": map[string]any{"type": "string", "pattern": "^[A-Za-z_][A-Za-z0-9_.-]{0,127}$"},
		},
		"count":      map[string]any{"type": "integer", "minimum": 1, "maximum": 512},
		"import_id":  map[string]any{"type": "string", "pattern": "^[a-f0-9]{32}$"},
		"request_id": requestResult,
	}, "imported", "count", "import_id", "request_id")

	tool.Description = "Mutate application secret metadata without accepting raw credential values over MCP. set_public is explicitly non-sensitive configuration; generate creates private values inside the runtime; import_staged consumes a private staged dotenv file. Every mutation requires an observed vault revision."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{
		"type":  "object",
		"oneOf": []any{createOutput, importOutput, setOutput, generateOutput},
	}
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = false
		tool.Annotations.DestructiveHint = boolPointer(true)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(false)
	}
	replay := func(reference string) OperationSemantics {
		return OperationSemantics{
			Replay: ReplayRequestID, RequestIDField: "request_id",
			ConcurrencyFields: []string{"expected_revision"},
			FailureAtomicity:  FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: reference,
		}
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"create_profile": replay("secret_inspect action=profiles; retry the same request_id while retained"),
		"set_public":     replay("secret_inspect action=profile; retry the same request_id while retained"),
		"generate":       replay("secret_inspect action=profile; retry the same request_id while retained"),
		"import_staged": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			ConcurrencyFields: []string{"expected_revision"},
			FailureAtomicity:  FailureNone, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 2,
			RecoveryReference:     "retry the same request_id while retained; inspect secret_inspect action=profile and action=imports if cleanup status is uncertain",
		},
	})
}

func overrideSecretDelete(tool *mcp.Tool) error {
	profile := secretProfileFieldSchema()
	expected := secretExpectedRevisionSchema()
	requestID := RequestIDSchema("Caller-owned UUID used to replay one destructive secret mutation after a lost response.")
	secretName := secretNameFieldSchema()
	input, err := (ActionInputContract{
		Title:             "secret_deleteArguments",
		ActionDescription: "Guarded encrypted-vault deletion to perform.",
		Fields: []ActionField{
			{Name: "profile", Schema: profile},
			{Name: "expected_revision", Schema: expected},
			{Name: "request_id", Schema: requestID},
			{Name: "secret", Schema: secretName},
		},
		Variants: []ActionVariant{
			{Name: "profile", Required: []string{"profile", "expected_revision", "request_id"}},
			{Name: "secret", Required: []string{"profile", "expected_revision", "request_id", "secret"}},
		},
	}).Schema()
	if err != nil {
		return err
	}
	requestResult := map[string]any{"type": "string", "pattern": RequestIDPattern}
	profileOutput := secretMutationBaseOutput(map[string]any{
		"removed":    map[string]any{"const": true},
		"request_id": requestResult,
	}, "removed", "request_id")
	secretOutput := secretMutationBaseOutput(map[string]any{
		"secret":     map[string]any{"type": "string"},
		"removed":    map[string]any{"const": true},
		"request_id": requestResult,
	}, "secret", "removed", "request_id")

	tool.Description = "Delete an application secret or whole profile using the observed vault revision and caller-owned request_id. Repeating the same request_id replays the committed terminal result instead of deleting a different later state."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{"type": "object", "oneOf": []any{profileOutput, secretOutput}}
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = false
		tool.Annotations.DestructiveHint = boolPointer(true)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(false)
	}
	replay := func(reference string) OperationSemantics {
		return OperationSemantics{
			Replay: ReplayRequestID, RequestIDField: "request_id",
			ConcurrencyFields: []string{"expected_revision"},
			FailureAtomicity:  FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: reference,
		}
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"profile": replay("secret_inspect action=profiles; retry the same request_id while retained"),
		"secret":  replay("secret_inspect action=profile; retry the same request_id while retained"),
	})
}
