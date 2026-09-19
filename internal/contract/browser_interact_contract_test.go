package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBrowserInteractUsesGenerationGuardedDiscriminatedContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "browser_interact")
	if tool == nil {
		t.Fatal("browser_interact definition missing")
	}
	if !strings.Contains(tool.Description, "expected_browser_generation") ||
		!strings.Contains(tool.Description, "expected_state_generation") ||
		!strings.Contains(tool.Description, "Stale") {
		t.Fatalf("browser_interact description = %q", tool.Description)
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
			t.Errorf("browser_interact property %s has no description", name)
		}
	}
	branches := input["oneOf"].([]any)
	if len(branches) != 8 {
		t.Fatalf("browser_interact branches = %#v", branches)
	}
	clickBranches := 0
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		if !required["action"] || !required["expected_browser_generation"] {
			t.Fatalf("%s required = %#v", action, required)
		}
		switch action {
		case "click":
			clickBranches++
			if required["index"] {
				if !required["expected_state_generation"] {
					t.Fatal("element click lacks state generation")
				}
				if _, exists := branchProperties["x"]; exists {
					t.Fatal("element click accepts coordinates")
				}
			} else {
				if !required["x"] || !required["y"] {
					t.Fatalf("coordinate click required = %#v", required)
				}
				if _, exists := branchProperties["expected_state_generation"]; exists {
					t.Fatal("coordinate click accepts state generation")
				}
				if _, exists := branchProperties["index"]; exists {
					t.Fatal("coordinate click accepts element index")
				}
			}
		case "type":
			for _, name := range []string{"expected_state_generation", "index", "text"} {
				if !required[name] {
					t.Errorf("type does not require %s", name)
				}
			}
		case "press":
			if !required["key"] {
				t.Fatal("press does not require key")
			}
		case "switch_tab", "close_tab":
			if !required["tab_id"] {
				t.Fatalf("%s does not require tab_id", action)
			}
		}
	}
	if clickBranches != 2 {
		t.Fatalf("click branches = %d", clickBranches)
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
	if len(outputBranches) != 7 {
		t.Fatalf("browser_interact output branches = %#v", outputBranches)
	}
	for _, raw := range outputBranches {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("browser_interact output remains open: %#v", branch)
		}
		if _, exists := branch["properties"].(map[string]any)["browser_generation"]; !exists {
			t.Fatalf("browser_interact output omits generation: %#v", branch)
		}
	}

	operations, ok := tool.Meta["loki/operations"].(map[string]any)
	if !ok || len(operations) != 7 {
		t.Fatalf("browser_interact operation metadata = %#v", tool.Meta)
	}
	for _, action := range []string{"click", "type", "press", "scroll", "back", "switch_tab", "close_tab"} {
		semantics := operations[action].(map[string]any)
		if semantics["replay"] != string(ReplayGuarded) ||
			semantics["failure_atomicity"] != string(FailureSingleResource) ||
			semantics["crash_recovery"] != string(CrashRecoveryInspect) {
			t.Fatalf("%s semantics = %#v", action, semantics)
		}
	}
	typeGuards := operations["type"].(map[string]any)["concurrency_fields"].([]string)
	if len(typeGuards) != 2 || typeGuards[0] != "expected_browser_generation" || typeGuards[1] != "expected_state_generation" {
		t.Fatalf("type guards = %#v", typeGuards)
	}
}
