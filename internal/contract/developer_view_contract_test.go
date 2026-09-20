package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeveloperViewUsesCapturedPresentationContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "developer_view")
	if tool == nil {
		t.Fatal("developer_view definition missing")
	}
	if !strings.Contains(tool.Description, "never reruns repository inspection") {
		t.Fatalf("developer_view description = %q", tool.Description)
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
		t.Fatalf("developer_view input remains open: %#v", input)
	}
	for name, raw := range input["properties"].(map[string]any) {
		if description, _ := raw.(map[string]any)["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("developer_view property %s lacks description", name)
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
		case "git_diff":
			if !required["content"] || !required["truncated"] {
				t.Fatalf("git_diff required = %#v", required)
			}
			if _, exists := properties["report_path"]; exists {
				t.Fatal("git_diff accepts report_path")
			}
		case "test_report":
			if !required["report_path"] {
				t.Fatalf("test_report required = %#v", required)
			}
			for _, irrelevant := range []string{"content", "truncated", "cwd", "path", "staged"} {
				if _, exists := properties[irrelevant]; exists {
					t.Fatalf("test_report accepts %s", irrelevant)
				}
			}
		default:
			t.Fatalf("unexpected developer_view action %q", action)
		}
	}
	if counts["git_diff"] != 1 || counts["test_report"] != 1 {
		t.Fatalf("developer_view branches = %#v", counts)
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
		t.Fatalf("developer_view outputs = %#v", branches)
	}
	kinds := map[string]bool{}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("developer_view output remains open: %#v", branch)
		}
		properties := branch["properties"].(map[string]any)
		kind := properties["kind"].(map[string]any)["const"].(string)
		kinds[kind] = true
		if kind == "diff" {
			stats := properties["stats"].(map[string]any)
			if stats["additionalProperties"] != false {
				t.Fatalf("diff stats remain open: %#v", stats)
			}
		}
	}
	if !kinds["diff"] || !kinds["test"] {
		t.Fatalf("developer_view output kinds = %#v", kinds)
	}

	hint := func(value *bool) bool { return value != nil && *value }
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint ||
		hint(tool.Annotations.DestructiveHint) || hint(tool.Annotations.OpenWorldHint) {
		t.Fatalf("developer_view annotations = %#v", tool.Annotations)
	}
}
