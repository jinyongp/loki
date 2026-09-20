package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArtifactPublishUsesDiscriminatedReplaySafeContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "artifact_publish")
	if tool == nil {
		t.Fatal("artifact_publish definition missing")
	}
	if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
		t.Fatalf("artifact_publish annotations = %#v", tool.Annotations)
	}
	hint := func(value *bool) bool { return value != nil && *value }
	if hint(tool.Annotations.DestructiveHint) || !hint(tool.Annotations.OpenWorldHint) {
		t.Fatalf("artifact_publish annotations = %#v", tool.Annotations)
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
		t.Fatalf("artifact_publish input remains open: %#v", input)
	}
	properties := input["properties"].(map[string]any)
	for name, raw := range properties {
		property := raw.(map[string]any)
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("artifact_publish property %s has no description", name)
		}
	}
	branches := input["oneOf"].([]any)
	if len(branches) != 2 {
		t.Fatalf("artifact_publish branches = %#v", branches)
	}
	found := map[string]bool{}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		found[action] = true
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		if !required["action"] || !required["request_id"] {
			t.Fatalf("%s required = %#v", action, required)
		}
		switch action {
		case "file":
			if !required["path"] {
				t.Fatal("file publication does not require path")
			}
			for _, irrelevant := range []string{"paths", "filename"} {
				if _, exists := branchProperties[irrelevant]; exists {
					t.Fatalf("file publication accepts %s", irrelevant)
				}
			}
		case "bundle":
			if !required["paths"] {
				t.Fatal("bundle publication does not require paths")
			}
			if _, exists := branchProperties["path"]; exists {
				t.Fatal("bundle publication accepts path")
			}
			if filename := branchProperties["filename"].(map[string]any); filename["default"] != nil {
				t.Fatalf("bundle-only filename default leaks through root defaults: %#v", filename)
			}
		default:
			t.Fatalf("unexpected artifact action %q", action)
		}
	}
	if !found["file"] || !found["bundle"] {
		t.Fatalf("artifact variants = %#v", found)
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
	if len(outputBranches) != 2 {
		t.Fatalf("artifact outputs = %#v", outputBranches)
	}
	outputKinds := map[string]bool{}
	for _, raw := range outputBranches {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("artifact output branch remains open: %#v", branch)
		}
		outputProperties := branch["properties"].(map[string]any)
		kind := outputProperties["kind"].(map[string]any)["const"].(string)
		outputKinds[kind] = true
		if kind == "file" {
			if _, ok := outputProperties["path"]; !ok {
				t.Fatal("file output omits path")
			}
			if _, ok := outputProperties["paths"]; ok {
				t.Fatal("file output contains bundle paths")
			}
		}
		if kind == "bundle" {
			for _, name := range []string{"paths", "file_count", "input_bytes", "excluded_entries"} {
				if _, ok := outputProperties[name]; !ok {
					t.Errorf("bundle output omits %s", name)
				}
			}
		}
	}
	if !outputKinds["file"] || !outputKinds["bundle"] {
		t.Fatalf("artifact output kinds = %#v", outputKinds)
	}

	operations, ok := tool.Meta["loki/operations"].(map[string]any)
	if !ok || len(operations) != 2 {
		t.Fatalf("artifact operation metadata = %#v", tool.Meta)
	}
	for _, action := range []string{"file", "bundle"} {
		semantics := operations[action].(map[string]any)
		if semantics["replay"] != string(ReplayRequestID) ||
			semantics["request_id_field"] != "request_id" ||
			semantics["failure_atomicity"] != string(FailureSingleResource) ||
			semantics["crash_recovery"] != string(CrashRecoveryNone) {
			t.Fatalf("%s semantics = %#v", action, semantics)
		}
	}
}
