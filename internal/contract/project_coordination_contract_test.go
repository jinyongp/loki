package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectCoordinationUsesActionSpecificReadContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "project_coordination")
	if tool == nil {
		t.Fatal("project_coordination definition missing")
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
		t.Fatalf("project_coordination input remains open: %#v", input)
	}
	for name, raw := range input["properties"].(map[string]any) {
		if description, _ := raw.(map[string]any)["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("project_coordination property %s lacks description", name)
		}
	}

	wantRequired := map[string][]string{
		"next":               {},
		"task_show":          {"task_id"},
		"current":            {},
		"task_context":       {"task_id"},
		"task_history":       {"task_id"},
		"checkpoint_list":    {"run_id"},
		"workstream_list":    {},
		"workstream_show":    {"workstream_id"},
		"workstream_context": {"workstream_id"},
		"workstream_history": {"workstream_id"},
	}
	seen := map[string]bool{}
	for _, raw := range input["oneOf"].([]any) {
		branch := raw.(map[string]any)
		properties := branch["properties"].(map[string]any)
		action := properties["action"].(map[string]any)["const"].(string)
		seen[action] = true
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		if !required["action"] {
			t.Fatalf("%s does not require action", action)
		}
		for _, name := range wantRequired[action] {
			if !required[name] {
				t.Errorf("%s does not require %s", action, name)
			}
		}
		switch action {
		case "current", "task_history", "checkpoint_list", "workstream_list", "workstream_history":
			if _, ok := properties["limit"]; !ok {
				t.Errorf("%s omits pagination", action)
			}
		default:
			if _, ok := properties["limit"]; ok {
				t.Errorf("%s unexpectedly accepts pagination", action)
			}
		}
	}
	if len(seen) != len(wantRequired) {
		t.Fatalf("project_coordination actions = %#v", seen)
	}

	encoded, err = json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	branches := output["oneOf"].([]any)
	if len(branches) != 4 {
		t.Fatalf("project_coordination output variants = %d, want 4", len(branches))
	}
	foundPage, foundContext := false, false
	for _, raw := range branches {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("project_coordination output remains open: %#v", branch)
		}
		properties := branch["properties"].(map[string]any)
		if _, ok := properties["next_cursor"]; ok {
			foundPage = true
			if _, ok := properties["complete"]; !ok {
				t.Fatal("paginated output omits completeness")
			}
		}
		if _, ok := properties["documents"]; ok {
			foundContext = true
			for _, name := range []string{"tasks", "validations", "history", "omitted_ids", "complete"} {
				if _, ok := properties[name]; !ok {
					t.Errorf("context output omits %s", name)
				}
			}
		}
	}
	if !foundPage || !foundContext {
		t.Fatalf("project_coordination outputs lack page/context semantics: page=%v context=%v", foundPage, foundContext)
	}
	hint := func(value *bool) bool { return value != nil && *value }
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint ||
		hint(tool.Annotations.DestructiveHint) || hint(tool.Annotations.OpenWorldHint) {
		t.Fatalf("project_coordination annotations = %#v", tool.Annotations)
	}
}
