package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBrowserObserveUsesGenerationAndCompletenessAwareContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "browser_observe")
	if tool == nil {
		t.Fatal("browser_observe definition missing")
	}
	if !strings.Contains(tool.Description, "state_generation") || !strings.Contains(tool.Description, "complete") {
		t.Fatalf("browser_observe description = %q", tool.Description)
	}

	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	if _, required := input["required"]; required {
		t.Fatalf("default state unexpectedly requires action: %#v", input)
	}
	properties := input["properties"].(map[string]any)
	if properties["action"].(map[string]any)["default"] != "state" {
		t.Fatalf("browser_observe action = %#v", properties["action"])
	}
	for name, raw := range properties {
		property := raw.(map[string]any)
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("browser_observe property %s has no description", name)
		}
	}
	branches := input["oneOf"].([]any)
	if len(branches) != 9 {
		t.Fatalf("browser_observe branches = %#v", branches)
	}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		switch action {
		case "state", "tabs", "dialog":
			if len(branchProperties) != 1 {
				t.Fatalf("%s accepts filters: %#v", action, branchProperties)
			}
		case "console":
			if _, exists := branchProperties["status_min"]; exists {
				t.Fatal("console accepts network status filter")
			}
		case "network":
			if _, exists := branchProperties["level"]; exists {
				t.Fatal("network accepts console level filter")
			}
		case "request":
			required := map[string]bool{}
			for _, item := range branch["required"].([]any) {
				required[item.(string)] = true
			}
			if !required["request_id"] || !required["action"] {
				t.Fatalf("request required = %#v", required)
			}
			if _, exists := branchProperties["limit"]; exists {
				t.Fatal("request accepts event limit")
			}
		case "diagnostics":
			limit := branchProperties["limit"].(map[string]any)
			if maximum, ok := limit["maximum"].(float64); !ok || maximum != 200 {
				t.Fatalf("diagnostics limit = %#v", limit)
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
	if len(outputBranches) != 8 {
		t.Fatalf("browser_observe output branches = %#v", outputBranches)
	}
	foundState := false
	foundEvent := false
	foundDialog := false
	for _, raw := range outputBranches {
		branch := raw.(map[string]any)
		if nested, ok := branch["oneOf"].([]any); ok {
			foundDialog = true
			if len(nested) != 3 {
				t.Fatalf("dialog output variants = %#v", nested)
			}
			for _, dialogRaw := range nested {
				dialogBranch := dialogRaw.(map[string]any)
				if dialogBranch["additionalProperties"] != false {
					t.Fatalf("dialog output branch is open: %#v", dialogBranch)
				}
				properties := dialogBranch["properties"].(map[string]any)
				if _, ok := properties["dialog_generation"]; !ok {
					t.Fatalf("dialog output lacks dialog_generation: %#v", dialogBranch)
				}
				if _, ok := properties["browser_generation"]; !ok {
					t.Fatalf("dialog output lacks browser_generation: %#v", dialogBranch)
				}
			}
			continue
		}
		if branch["additionalProperties"] != false {
			t.Fatalf("browser_observe output branch is open: %#v", branch)
		}
		branchProperties := branch["properties"].(map[string]any)
		if _, ok := branchProperties["state_generation"]; ok {
			foundState = true
			if _, ok := branchProperties["browser_generation"]; !ok {
				t.Fatal("state output lacks browser_generation")
			}
		}
		if _, ok := branchProperties["complete"]; ok {
			foundEvent = true
			if _, ok := branchProperties["next_sequence"]; !ok {
				t.Fatal("event output lacks next_sequence")
			}
		}
	}
	if !foundState || !foundEvent || !foundDialog {
		t.Fatalf("browser observe outputs missing semantics: state=%v event=%v dialog=%v", foundState, foundEvent, foundDialog)
	}
}
