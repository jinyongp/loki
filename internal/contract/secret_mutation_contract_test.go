package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSecretMutationContractsAreGuardedAndDiscriminated(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	write := seenDefinition(definitions, "secret_write")
	remove := seenDefinition(definitions, "secret_delete")
	if write == nil || remove == nil {
		t.Fatalf("secret mutation contracts missing: write=%#v delete=%#v", write, remove)
	}
	hint := func(value *bool) bool { return value != nil && *value }
	if write.Annotations == nil || write.Annotations.ReadOnlyHint || !hint(write.Annotations.DestructiveHint) ||
		!write.Annotations.IdempotentHint || hint(write.Annotations.OpenWorldHint) {
		t.Fatalf("secret_write annotations = %#v", write.Annotations)
	}
	if remove.Annotations == nil || remove.Annotations.ReadOnlyHint || !hint(remove.Annotations.DestructiveHint) ||
		!remove.Annotations.IdempotentHint || hint(remove.Annotations.OpenWorldHint) {
		t.Fatalf("secret_delete annotations = %#v", remove.Annotations)
	}

	checkFields := func(tool *mcp.Tool) map[string]any {
		t.Helper()
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var input map[string]any
		if err := json.Unmarshal(encoded, &input); err != nil {
			t.Fatal(err)
		}
		if input["additionalProperties"] != false {
			t.Fatalf("%s input remains open", tool.Name)
		}
		for name, raw := range input["properties"].(map[string]any) {
			if description, _ := raw.(map[string]any)["description"].(string); strings.TrimSpace(description) == "" {
				t.Errorf("%s property %s lacks description", tool.Name, name)
			}
		}
		return input
	}
	writeInput := checkFields(write)
	deleteInput := checkFields(remove)

	writeCounts := map[string]int{}
	for _, raw := range writeInput["oneOf"].([]any) {
		branch := raw.(map[string]any)
		properties := branch["properties"].(map[string]any)
		action := properties["action"].(map[string]any)["const"].(string)
		writeCounts[action]++
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		if !required["profile"] || !required["expected_revision"] {
			t.Fatalf("%s write guards = %#v", action, required)
		}
		switch action {
		case "create_profile":
			if !required["request_id"] || len(properties) != 4 {
				t.Fatalf("create_profile shape = %#v", properties)
			}
		case "import_staged":
			for _, field := range []string{"request_id", "import_id"} {
				if !required[field] {
					t.Errorf("import_staged does not require %s", field)
				}
			}
		case "set_public":
			for _, field := range []string{"request_id", "name", "value"} {
				if !required[field] {
					t.Errorf("set_public does not require %s", field)
				}
			}
		case "generate":
			for _, field := range []string{"request_id", "secret"} {
				if !required[field] {
					t.Errorf("generate does not require %s", field)
				}
			}
			if required["bytes"] {
				t.Fatal("generate unexpectedly requires bytes")
			}
		default:
			t.Fatalf("unexpected secret_write action %q", action)
		}
	}
	for _, action := range []string{"create_profile", "import_staged", "set_public", "generate"} {
		if writeCounts[action] != 1 {
			t.Errorf("secret_write %s branches = %d", action, writeCounts[action])
		}
	}
	for _, removedAction := range []string{"set", "import_env"} {
		if writeCounts[removedAction] != 0 {
			t.Errorf("legacy secret_write action remains: %s", removedAction)
		}
	}

	deleteCounts := map[string]int{}
	for _, raw := range deleteInput["oneOf"].([]any) {
		branch := raw.(map[string]any)
		properties := branch["properties"].(map[string]any)
		action := properties["action"].(map[string]any)["const"].(string)
		deleteCounts[action]++
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		for _, field := range []string{"profile", "expected_revision", "request_id"} {
			if !required[field] {
				t.Errorf("%s delete does not require %s", action, field)
			}
		}
		if action == "secret" && !required["secret"] {
			t.Fatal("secret deletion does not require secret")
		}
		if action == "profile" {
			if _, exists := properties["secret"]; exists {
				t.Fatal("profile deletion accepts secret")
			}
		}
	}
	if deleteCounts["profile"] != 1 || deleteCounts["secret"] != 1 {
		t.Fatalf("secret_delete branches = %#v", deleteCounts)
	}

	for _, tool := range []*mcp.Tool{write, remove} {
		encoded, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var output map[string]any
		if err := json.Unmarshal(encoded, &output); err != nil {
			t.Fatal(err)
		}
		for _, raw := range output["oneOf"].([]any) {
			if raw.(map[string]any)["additionalProperties"] != false {
				t.Fatalf("%s output remains open: %#v", tool.Name, raw)
			}
		}
	}

	writeOps := write.Meta["loki/operations"].(map[string]any)
	for _, action := range []string{"create_profile", "import_staged", "set_public", "generate"} {
		semantics := writeOps[action].(map[string]any)
		if semantics["replay"] != string(ReplayRequestID) || semantics["request_id_field"] != "request_id" {
			t.Fatalf("%s replay metadata = %#v", action, semantics)
		}
	}
	importSemantics := writeOps["import_staged"].(map[string]any)
	if importSemantics["replay"] != string(ReplayRequestID) ||
		importSemantics["request_id_field"] != "request_id" ||
		importSemantics["failure_atomicity"] != string(FailureNone) {
		t.Fatalf("import_staged metadata = %#v", importSemantics)
	}
	deleteOps := remove.Meta["loki/operations"].(map[string]any)
	for _, action := range []string{"profile", "secret"} {
		semantics := deleteOps[action].(map[string]any)
		if semantics["replay"] != string(ReplayRequestID) || semantics["request_id_field"] != "request_id" {
			t.Fatalf("%s delete metadata = %#v", action, semantics)
		}
	}
}
