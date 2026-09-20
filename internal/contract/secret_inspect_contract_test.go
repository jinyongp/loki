package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretInspectUsesDiscriminatedMetadataContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "secret_inspect")
	if tool == nil {
		t.Fatal("secret_inspect definition missing")
	}
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
		t.Fatalf("secret_inspect annotations = %#v", tool.Annotations)
	}
	hint := func(value *bool) bool { return value != nil && *value }
	if hint(tool.Annotations.DestructiveHint) || hint(tool.Annotations.OpenWorldHint) {
		t.Fatalf("secret_inspect annotations = %#v", tool.Annotations)
	}

	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	if input["additionalProperties"] != false {
		t.Fatalf("secret_inspect input remains open: %#v", input)
	}
	for name, raw := range input["properties"].(map[string]any) {
		property := raw.(map[string]any)
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("secret_inspect property %s has no description", name)
		}
	}
	branches := input["oneOf"].([]any)
	if len(branches) != 5 {
		t.Fatalf("secret_inspect branches = %d, want 5", len(branches))
	}
	found := map[string]bool{}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		properties := branch["properties"].(map[string]any)
		action := properties["action"].(map[string]any)["const"].(string)
		found[action] = true
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		switch action {
		case "profiles", "imports", "audit":
			if required["offset"] || required["limit"] {
				t.Fatalf("%s pagination unexpectedly required: %#v", action, required)
			}
			if _, ok := properties["profile"]; ok {
				t.Fatalf("%s accepts profile", action)
			}
		case "profile":
			if !required["profile"] {
				t.Fatal("profile action does not require profile")
			}
			if _, ok := properties["offset"]; ok {
				t.Fatal("profile action accepts offset")
			}
		case "status":
			if len(properties) != 1 {
				t.Fatalf("status accepts irrelevant fields: %#v", properties)
			}
		default:
			t.Fatalf("unexpected secret inspect action %q", action)
		}
	}
	for _, action := range []string{"profiles", "imports", "profile", "status", "audit"} {
		if !found[action] {
			t.Errorf("secret_inspect action %s missing", action)
		}
	}

	encoded, err = json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	outputs := output["oneOf"].([]any)
	if len(outputs) != 5 {
		t.Fatalf("secret_inspect output branches = %d, want 5", len(outputs))
	}
	foundProfiles, foundImports, foundProfile, foundStatus, foundAudit := false, false, false, false, false
	for _, raw := range outputs {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("secret_inspect output remains open: %#v", branch)
		}
		properties := branch["properties"].(map[string]any)
		switch {
		case properties["initialized"] != nil:
			foundStatus = true
			if properties["revision"] == nil || properties["policy_generation"] == nil ||
				properties["devtools"] == nil || properties["github"] == nil {
				t.Fatalf("status output incomplete: %#v", properties)
			}
		case properties["profiles"] != nil:
			foundProfiles = true
			if properties["revision"] == nil || properties["complete"] == nil || properties["next_offset"] == nil {
				t.Fatalf("profiles output incomplete: %#v", properties)
			}
		case properties["imports"] != nil:
			foundImports = true
			if properties["complete"] == nil || properties["next_offset"] == nil {
				t.Fatalf("imports output incomplete: %#v", properties)
			}
		case properties["name"] != nil:
			foundProfile = true
			if properties["revision"] == nil {
				t.Fatal("profile output omits revision")
			}
		case properties["records"] != nil:
			foundAudit = true
			if properties["complete"] == nil || properties["next_offset"] == nil {
				t.Fatalf("audit output incomplete: %#v", properties)
			}
		default:
			t.Fatalf("unknown secret_inspect output: %#v", properties)
		}
	}
	if !foundProfiles || !foundImports || !foundProfile || !foundStatus || !foundAudit {
		t.Fatalf("secret outputs missing: profiles=%v imports=%v profile=%v status=%v audit=%v",
			foundProfiles, foundImports, foundProfile, foundStatus, foundAudit)
	}
}
