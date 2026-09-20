package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentGuidanceUsesDiscriminatedClosedContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "agent_guidance")
	if tool == nil {
		t.Fatal("agent_guidance definition missing")
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
		t.Fatalf("agent_guidance input remains open: %#v", input)
	}
	for name, raw := range input["properties"].(map[string]any) {
		if description, _ := raw.(map[string]any)["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("agent_guidance property %s lacks description", name)
		}
	}
	counts := map[string]int{}
	for _, raw := range input["oneOf"].([]any) {
		branch := raw.(map[string]any)
		properties := branch["properties"].(map[string]any)
		action := properties["action"].(map[string]any)["const"].(string)
		counts[action]++
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		switch action {
		case "context":
			if _, exists := properties["name"]; exists {
				t.Fatal("context accepts Skill name")
			}
		case "skill":
			if !required["name"] {
				t.Fatalf("skill required = %#v", required)
			}
		default:
			t.Fatalf("unexpected action %q", action)
		}
	}
	if counts["context"] != 1 || counts["skill"] != 1 {
		t.Fatalf("agent_guidance branches = %#v", counts)
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
	if len(branches) != 2 {
		t.Fatalf("agent_guidance outputs = %#v", branches)
	}
	foundContext, foundSkill := false, false
	for _, raw := range branches {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("agent_guidance output remains open: %#v", branch)
		}
		properties := branch["properties"].(map[string]any)
		if guidance, ok := properties["guidance"].(map[string]any); ok {
			foundContext = true
			if guidance["additionalProperties"] != false {
				t.Fatalf("guidance remains open: %#v", guidance)
			}
			skills := properties["skills"].(map[string]any)
			if skills["additionalProperties"] != false {
				t.Fatalf("skill catalog remains open: %#v", skills)
			}
		}
		if skill, ok := properties["skill"].(map[string]any); ok {
			foundSkill = true
			if skill["additionalProperties"] != false || properties["complete"].(map[string]any)["const"] != true {
				t.Fatalf("skill inspection output = %#v", properties)
			}
		}
	}
	if !foundContext || !foundSkill {
		t.Fatalf("agent_guidance output variants missing: context=%v skill=%v", foundContext, foundSkill)
	}
	hint := func(value *bool) bool { return value != nil && *value }
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint ||
		hint(tool.Annotations.DestructiveHint) || hint(tool.Annotations.OpenWorldHint) {
		t.Fatalf("agent_guidance annotations = %#v", tool.Annotations)
	}
}
