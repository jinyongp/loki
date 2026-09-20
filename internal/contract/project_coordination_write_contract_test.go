package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectCoordinationWriteUsesTransitionSpecificContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "project_coordination_write")
	if tool == nil {
		t.Fatal("project_coordination_write definition missing")
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
		t.Fatalf("project_coordination_write input remains open: %#v", input)
	}
	for name, raw := range input["properties"].(map[string]any) {
		if description, _ := raw.(map[string]any)["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("project_coordination_write property %s lacks description", name)
		}
	}
	branches := input["oneOf"].([]any)
	if len(branches) != 8 {
		t.Fatalf("write branches = %d, want 8", len(branches))
	}
	counts := map[string]int{}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		properties := branch["properties"].(map[string]any)
		action := properties["action"].(map[string]any)["const"].(string)
		counts[action]++
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		if !required["action"] || !required["request_id"] {
			t.Fatalf("%s required = %#v", action, required)
		}
		switch action {
		case "takeover":
			if !required["task_id"] || !required["expected_run_id"] {
				t.Fatalf("takeover required = %#v", required)
			}
		case "resume", "done":
			if !required["task_id"] {
				t.Fatalf("%s required = %#v", action, required)
			}
		case "checkpoint", "release":
			if !required["run_id"] {
				t.Fatalf("%s required = %#v", action, required)
			}
		}
		if action == "checkpoint" || action == "done" {
			if !required["summary"] {
				t.Fatalf("%s does not require summary", action)
			}
		}
	}
	if counts["claim"] != 3 || counts["takeover"] != 1 || counts["resume"] != 1 ||
		counts["checkpoint"] != 1 || counts["release"] != 1 || counts["done"] != 1 {
		t.Fatalf("write branch counts = %#v", counts)
	}

	encoded, err = json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if output["additionalProperties"] != false {
		t.Fatalf("write output remains open: %#v", output)
	}
	properties := output["properties"].(map[string]any)
	for _, name := range []string{"action", "request_id", "profile", "revision", "details"} {
		if _, ok := properties[name]; !ok {
			t.Errorf("write output omits %s", name)
		}
	}
	if details := properties["details"].(map[string]any); details["type"] != "object" {
		t.Fatalf("write details = %#v", details)
	}

	operations := tool.Meta["loki/operations"].(map[string]any)
	if len(operations) != 6 {
		t.Fatalf("write operation metadata = %#v", operations)
	}
	for _, action := range []string{"claim", "takeover", "resume", "checkpoint", "release", "done"} {
		semantics := operations[action].(map[string]any)
		if semantics["replay"] != string(ReplayRequestID) || semantics["request_id_field"] != "request_id" ||
			semantics["failure_atomicity"] != string(FailureUpstream) ||
			semantics["crash_recovery"] != string(CrashRecoveryUpstream) {
			t.Fatalf("%s semantics = %#v", action, semantics)
		}
	}
	takeover := operations["takeover"].(map[string]any)
	guards := takeover["concurrency_fields"].([]string)
	if len(guards) != 1 || guards[0] != "expected_run_id" {
		t.Fatalf("takeover guards = %#v", guards)
	}
	hint := func(value *bool) bool { return value != nil && *value }
	if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || !hint(tool.Annotations.DestructiveHint) ||
		!tool.Annotations.IdempotentHint || hint(tool.Annotations.OpenWorldHint) {
		t.Fatalf("project_coordination_write annotations = %#v", tool.Annotations)
	}
}
