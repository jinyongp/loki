package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestSecretV2RejectsLegacyAndCrossActionInputs(t *testing.T) {
	for _, tool := range []string{"secret_inspect", "secret_write", "secret_delete"} {
		input, err := SecretV2InputSchema(tool)
		if err != nil {
			t.Fatal(err)
		}
		definitions, err := CurrentDefinitions()
		if err != nil {
			t.Fatal(err)
		}
		active := seenDefinition(definitions, tool)
		candidateJSON, _ := json.Marshal(input)
		activeJSON, _ := json.Marshal(active.InputSchema)
		if string(candidateJSON) != string(activeJSON) {
			t.Fatal("active MCP contract differs from the resource contract", tool)
		}
		raw, _ := json.Marshal(input)
		var schema jsonschema.Schema
		if err = json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		var good map[string]any
		switch tool {
		case "secret_write":
			good = map[string]any{"action": "generate", "resource": "example", "name": "TOKEN", "expected_revision": strings.Repeat("a", 64), "request_id": "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2"}
		case "secret_delete":
			good = map[string]any{"action": "unset", "resource": "example", "kind": "secret", "name": "TOKEN", "expected_revision": strings.Repeat("a", 64), "request_id": "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2"}
		default:
			good = map[string]any{"action": "values", "resource": "example"}
		}
		if err = resolved.Validate(good); err != nil {
			t.Fatal(tool, err)
		}
		for key, value := range map[string]any{"profile": "github-app", "value": "private", "bytes": 32, "env": nil, "unknown": true, "expected_revision": 1, "request_id": "9AF6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2"} {
			candidate := map[string]any{}
			for k, v := range good {
				candidate[k] = v
			}
			candidate[key] = value
			if resolved.Validate(candidate) == nil {
				t.Fatalf("%s accepted %s=%v", tool, key, value)
			}
		}
		good["action"] = "create_profile"
		if resolved.Validate(good) == nil {
			t.Fatal("legacy profile action accepted")
		}
	}
}

func TestSecretV2OutputsRejectPrivateDataAndArbitraryMetadata(t *testing.T) {
	for _, tool := range []string{"secret_inspect", "secret_write", "secret_delete"} {
		raw, _ := json.Marshal(SecretV2OutputSchema(tool))
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(tool, err)
		}
		good := map[string]any{"action": "generate", "resource": "example", "env": "common", "revision": strings.Repeat("a", 64), "request_id": "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", "changed": true, "replayed": false, "name": "TOKEN", "bytes": 32}
		if tool == "secret_inspect" {
			good = map[string]any{"action": "status", "resource": "example", "state": "locked", "integrity_verified": false, "verification_pending": true}
		}
		if tool == "secret_delete" {
			good["action"] = "unset"
			good["kind"] = "secret"
			good["effective_present"] = false
			good["source"] = nil
			delete(good, "bytes")
		}
		if err = resolved.Validate(good); err != nil {
			t.Fatal(tool, err)
		}
		for _, field := range []string{"value", "patterns", "root_path", "input_id", "ticket", "secret_hash", "raw_error"} {
			candidate := map[string]any{}
			for k, v := range good {
				candidate[k] = v
			}
			candidate[field] = "private"
			if resolved.Validate(candidate) == nil {
				t.Fatalf("%s leaked %s", tool, field)
			}
		}
	}
}
