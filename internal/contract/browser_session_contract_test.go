package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBrowserSessionUsesGenerationAwareDiscriminatedContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "browser_session")
	if tool == nil {
		t.Fatal("browser_session definition missing")
	}
	if !strings.Contains(tool.Description, "browser_generation") {
		t.Fatalf("browser_session description = %q", tool.Description)
	}

	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	properties := input["properties"].(map[string]any)
	for name, raw := range properties {
		property := raw.(map[string]any)
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("browser_session property %s has no description", name)
		}
	}
	branches := input["oneOf"].([]any)
	if len(branches) != 3 {
		t.Fatalf("browser_session branches = %#v", branches)
	}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		required := map[string]bool{}
		for _, value := range branch["required"].([]any) {
			required[value.(string)] = true
		}
		switch action {
		case "start", "stop":
			if _, exists := branchProperties["url"]; exists {
				t.Fatalf("%s accepts url", action)
			}
			if _, exists := branchProperties["new_tab"]; exists {
				t.Fatalf("%s accepts new_tab", action)
			}
		case "navigate":
			if !required["url"] {
				t.Fatal("navigate does not require url")
			}
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
	outputBranches := output["oneOf"].([]any)
	if len(outputBranches) != 3 {
		t.Fatalf("browser_session output branches = %#v", outputBranches)
	}
	for _, raw := range outputBranches {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("browser_session output remains open: %#v", branch)
		}
		outputProperties := branch["properties"].(map[string]any)
		if _, exists := outputProperties["browser_generation"]; !exists {
			t.Fatalf("browser_session output omits generation: %#v", branch)
		}
	}

	operations, ok := tool.Meta["loki/operations"].(map[string]any)
	if !ok || len(operations) != 3 {
		t.Fatalf("browser_session operations = %#v", tool.Meta)
	}
	if operations["start"].(map[string]any)["replay"] != string(ReplayIdempotent) {
		t.Fatalf("start semantics = %#v", operations["start"])
	}
	if operations["navigate"].(map[string]any)["replay"] != string(ReplayUnsafe) {
		t.Fatalf("navigate semantics = %#v", operations["navigate"])
	}
	if operations["stop"].(map[string]any)["replay"] != string(ReplayIdempotent) {
		t.Fatalf("stop semantics = %#v", operations["stop"])
	}
}
