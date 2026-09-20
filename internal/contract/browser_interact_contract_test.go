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
	for _, phrase := range []string{"expected_browser_generation", "expected_state_generation", "fill", "type", "Stale"} {
		if !strings.Contains(tool.Description, phrase) {
			t.Fatalf("browser_interact description lacks %q: %q", phrase, tool.Description)
		}
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
	actionEnum := properties["action"].(map[string]any)["enum"].([]any)
	for _, want := range []string{"hover", "drag", "wheel", "fill", "type", "key", "shortcut", "select_option", "set_checked", "focus", "upload", "dialog"} {
		found := false
		for _, raw := range actionEnum {
			found = found || raw == want
		}
		if !found {
			t.Errorf("browser_interact action enum omits %s", want)
		}
	}
	for _, removed := range []string{"scroll", "press", "back"} {
		for _, raw := range actionEnum {
			if raw == removed {
				t.Errorf("legacy action %s remains public", removed)
			}
		}
	}

	branches := input["oneOf"].([]any)
	if len(branches) != 23 {
		t.Fatalf("browser_interact branches = %d, want 23", len(branches))
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
		case "fill", "type":
			for _, name := range []string{"expected_state_generation", "index", "text"} {
				if !required[name] {
					t.Errorf("%s does not require %s", action, name)
				}
			}
		case "key":
			if !required["key"] || required["modifiers"] {
				t.Fatalf("key required = %#v", required)
			}
		case "shortcut":
			if !required["key"] || !required["modifiers"] {
				t.Fatalf("shortcut required = %#v", required)
			}
			if minimum, _ := branchProperties["modifiers"].(map[string]any)["minItems"].(float64); minimum != 1 {
				t.Fatalf("shortcut modifiers = %#v", branchProperties["modifiers"])
			}
		case "select_option":
			for _, name := range []string{"expected_state_generation", "index", "options"} {
				if !required[name] {
					t.Errorf("select_option does not require %s", name)
				}
			}
		case "set_checked":
			for _, name := range []string{"expected_state_generation", "index", "checked"} {
				if !required[name] {
					t.Errorf("set_checked does not require %s", name)
				}
			}
		case "focus":
			if !required["expected_state_generation"] || !required["index"] {
				t.Fatalf("focus required = %#v", required)
			}
		case "upload":
			for _, name := range []string{"expected_state_generation", "index", "paths"} {
				if !required[name] {
					t.Errorf("upload does not require %s", name)
				}
			}
		case "dialog":
			for _, name := range []string{"expected_dialog_generation", "accept"} {
				if !required[name] {
					t.Errorf("dialog does not require %s", name)
				}
			}
			if _, hasPrompt := branchProperties["prompt_text"]; hasPrompt {
				if branchProperties["accept"].(map[string]any)["const"] != true {
					t.Fatalf("dialog prompt branch does not require accept=true: %#v", branchProperties["accept"])
				}
			}
		case "switch_tab", "close_tab":
			if !required["tab_id"] {
				t.Fatalf("%s does not require tab_id", action)
			}
		}
	}
	for action, want := range map[string]int{
		"click": 3, "hover": 2, "drag": 3, "wheel": 3,
		"fill": 1, "type": 1, "key": 1, "shortcut": 1,
		"select_option": 1, "set_checked": 1, "focus": 1, "upload": 1, "dialog": 2,
		"switch_tab": 1, "close_tab": 1,
	} {
		if counts[action] != want {
			t.Errorf("%s branches = %d, want %d", action, counts[action], want)
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
	if len(outputBranches) != 15 {
		t.Fatalf("browser_interact output branches = %d, want 15", len(outputBranches))
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
	if !ok || len(operations) != 15 {
		t.Fatalf("browser_interact operation metadata = %#v", tool.Meta)
	}
	for _, action := range []string{
		"click", "hover", "drag", "wheel", "fill", "type", "key", "shortcut",
		"select_option", "set_checked", "focus", "upload", "dialog", "switch_tab", "close_tab",
	} {
		semantics := operations[action].(map[string]any)
		if semantics["replay"] != string(ReplayGuarded) ||
			semantics["failure_atomicity"] != string(FailureSingleResource) ||
			semantics["crash_recovery"] != string(CrashRecoveryInspect) {
			t.Fatalf("%s semantics = %#v", action, semantics)
		}
	}
	for _, action := range []string{"fill", "type", "select_option", "set_checked", "focus", "upload"} {
		guards := operations[action].(map[string]any)["concurrency_fields"].([]string)
		if len(guards) != 2 || guards[0] != "expected_browser_generation" || guards[1] != "expected_state_generation" {
			t.Fatalf("%s guards = %#v", action, guards)
		}
	}
	dialogGuards := operations["dialog"].(map[string]any)["concurrency_fields"].([]string)
	if len(dialogGuards) != 2 || dialogGuards[0] != "expected_browser_generation" || dialogGuards[1] != "expected_dialog_generation" {
		t.Fatalf("dialog guards = %#v", dialogGuards)
	}
	for _, removed := range []string{"scroll", "press", "back"} {
		if _, exists := operations[removed]; exists {
			t.Errorf("legacy operation metadata remains for %s", removed)
		}
	}
}
