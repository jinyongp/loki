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
	if !strings.Contains(tool.Description, "browser_generation") || !strings.Contains(tool.Description, "generation-guarded") {
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
	if len(branches) != 7 {
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
			for _, forbidden := range []string{"url", "new_tab", "expected_browser_generation"} {
				if _, exists := branchProperties[forbidden]; exists {
					t.Fatalf("%s accepts %s", action, forbidden)
				}
			}
		case "navigate":
			if !required["url"] {
				t.Fatal("navigate does not require url")
			}
			if _, exists := branchProperties["expected_browser_generation"]; exists {
				t.Fatal("navigate unexpectedly accepts expected_browser_generation")
			}
		case "back", "forward", "reload", "stop_loading":
			if !required["expected_browser_generation"] {
				t.Fatalf("%s does not require expected_browser_generation", action)
			}
			if _, exists := branchProperties["url"]; exists {
				t.Fatalf("%s accepts url", action)
			}
			if _, exists := branchProperties["new_tab"]; exists {
				t.Fatalf("%s accepts new_tab", action)
			}
		default:
			t.Fatalf("unexpected browser_session action %q", action)
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
	if len(outputBranches) != 4 {
		t.Fatalf("browser_session output branches = %#v", outputBranches)
	}
	foundNavigation := false
	for _, raw := range outputBranches {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("browser_session output remains open: %#v", branch)
		}
		outputProperties := branch["properties"].(map[string]any)
		if _, exists := outputProperties["browser_generation"]; !exists {
			t.Fatalf("browser_session output omits generation: %#v", branch)
		}
		if navigation, exists := outputProperties["navigation"]; exists {
			foundNavigation = true
			values := navigation.(map[string]any)["enum"].([]any)
			if len(values) != 4 {
				t.Fatalf("navigation output enum = %#v", values)
			}
			if _, exists := outputProperties["performed"]; !exists {
				t.Fatal("navigation output omits performed")
			}
		}
	}
	if !foundNavigation {
		t.Fatal("browser_session output omits history/navigation result")
	}

	operations, ok := tool.Meta["loki/operations"].(map[string]any)
	if !ok || len(operations) != 7 {
		t.Fatalf("browser_session operations = %#v", tool.Meta)
	}
	if operations["start"].(map[string]any)["replay"] != string(ReplayIdempotent) {
		t.Fatalf("start semantics = %#v", operations["start"])
	}
	if operations["navigate"].(map[string]any)["replay"] != string(ReplayUnsafe) {
		t.Fatalf("navigate semantics = %#v", operations["navigate"])
	}
	for _, action := range []string{"back", "forward", "reload", "stop_loading"} {
		semantics := operations[action].(map[string]any)
		if semantics["replay"] != string(ReplayGuarded) {
			t.Fatalf("%s semantics = %#v", action, semantics)
		}
		guards := semantics["concurrency_fields"].([]string)
		if len(guards) != 1 || guards[0] != "expected_browser_generation" {
			t.Fatalf("%s guards = %#v", action, guards)
		}
	}
	if operations["stop"].(map[string]any)["replay"] != string(ReplayIdempotent) {
		t.Fatalf("stop semantics = %#v", operations["stop"])
	}
}
