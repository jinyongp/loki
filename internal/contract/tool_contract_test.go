package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActionInputContractRequiresDescribedFields(t *testing.T) {
	_, err := (ActionInputContract{
		Title:             "fixture",
		ActionDescription: "Fixture action.",
		Fields: []ActionField{{
			Name:   "path",
			Schema: map[string]any{"type": "string"},
		}},
		Variants: []ActionVariant{{Name: "read", Required: []string{"path"}}},
	}).Schema()
	if err == nil || !strings.Contains(err.Error(), "requires a description") {
		t.Fatalf("missing field description error = %v", err)
	}

	_, err = (ActionInputContract{
		Title:             "fixture",
		ActionDescription: "Fixture action.",
		Fields: []ActionField{{
			Name:   "path",
			Schema: map[string]any{"type": "string", "description": "Path."},
		}},
		Variants: []ActionVariant{{Name: "read", Required: []string{"missing"}}},
	}).Schema()
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown action field error = %v", err)
	}
}

func TestWorkspaceEditUsesGeneratedActionContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "workspace_edit")
	if tool == nil {
		t.Fatal("workspace_edit definition missing")
	}
	if !strings.Contains(tool.Description, "multiple files") || !strings.Contains(tool.Description, "patch-file limit") {
		t.Fatalf("workspace_edit description = %q", tool.Description)
	}

	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("workspace_edit properties = %#v", schema["properties"])
	}
	for name, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("workspace_edit property %s = %#v", name, raw)
		}
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("workspace_edit property %s has no description", name)
		}
	}
	branches, ok := schema["oneOf"].([]any)
	if !ok || len(branches) != 4 {
		t.Fatalf("workspace_edit oneOf = %#v", schema["oneOf"])
	}

	var replace map[string]any
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)
		if action["const"] == "replace" {
			replace = branch
			break
		}
	}
	if replace == nil {
		t.Fatal("replace action branch missing")
	}
	required := map[string]bool{}
	for _, raw := range replace["required"].([]any) {
		required[raw.(string)] = true
	}
	for _, name := range []string{"action", "path", "old", "new", "expected_sha256", "expected_replacements"} {
		if !required[name] {
			t.Errorf("replace action does not require %s", name)
		}
	}
}
