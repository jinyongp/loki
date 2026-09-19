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
	if _, exists := properties["scroll"]; exists {
		t.Fatal("legacy scroll field unexpectedly exists")
	}
	actionEnum := properties["action"].(map[string]any)["enum"].([]any)
	for _, want := range []string{"hover", "drag", "wheel"} {
		found := false
		for _, raw := range actionEnum {
			found = found || raw == want
		}
		if !found {
			t.Errorf("browser_interact action enum omits %s", want)
		}
	}

	branches := input["oneOf"].([]any)
	if len(branches) != 16 {
		t.Fatalf("browser_interact branches = %d, want 16", len(branches))
	}
	counts := map[string]int{}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		counts[action]++
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		if !required["action"] || !required["expected_browser_generation"] {
			t.Fatalf("%s required = %#v", action, required)
		}
		switch action {
		case "click":
			if required["index"] && !required["expected_state_generation"] {
				t.Fatal("element click lacks state generation")
			}
			if required["new_tab"] {
				for _, forbidden := range []string{"button", "click_count", "modifiers", "x", "y"} {
					if _, exists := branchProperties[forbidden]; exists {
						t.Fatalf("new_tab click accepts %s", forbidden)
					}
				}
			}
		case "hover":
			if required["index"] && !required["expected_state_generation"] {
				t.Fatal("element hover lacks state generation")
			}
		case "drag":
			if required["source_index"] && !required["expected_state_generation"] {
				t.Fatal("element drag lacks state generation")
			}
			if required["target_index"] && !required["source_index"] {
				t.Fatal("coordinate-to-element drag branch exists")
			}
		case "wheel":
			if !required["delta_x"] || !required["delta_y"] {
				t.Fatal("wheel does not require both deltas")
			}
			if required["index"] && !required["expected_state_generation"] {
				t.Fatal("element wheel lacks state generation")
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
	for action, want := range map[string]int{
		"click": 3, "hover": 2, "drag": 3, "wheel": 3,
		"type": 1, "press": 1, "back": 1, "switch_tab": 1, "close_tab": 1,
	} {
		if counts[action] != want {
			t.Errorf("%s branches = %d, want %d", action, counts[action], want)
		}
	}
	if counts["scroll"] != 0 {
		t.Fatal("legacy scroll action remains public")
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
	if len(outputBranches) != 9 {
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
	if !ok || len(operations) != 9 {
		t.Fatalf("browser_interact operation metadata = %#v", tool.Meta)
	}
	for _, action := range []string{"click", "hover", "drag", "wheel", "type", "press", "back", "switch_tab", "close_tab"} {
		semantics := operations[action].(map[string]any)
		if semantics["replay"] != string(ReplayGuarded) ||
			semantics["failure_atomicity"] != string(FailureSingleResource) ||
			semantics["crash_recovery"] != string(CrashRecoveryInspect) {
			t.Fatalf("%s semantics = %#v", action, semantics)
		}
	}
	if _, exists := operations["scroll"]; exists {
		t.Fatal("legacy scroll operation metadata remains")
	}
	typeGuards := operations["type"].(map[string]any)["concurrency_fields"].([]string)
	if len(typeGuards) != 2 || typeGuards[0] != "expected_browser_generation" || typeGuards[1] != "expected_state_generation" {
		t.Fatalf("type guards = %#v", typeGuards)
	}
}
