package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPreviewPublishUsesReplaySafeDiscriminatedContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "preview_publish")
	if tool == nil {
		t.Fatal("preview_publish definition missing")
	}
	if tool.Annotations == nil || !tool.Annotations.IdempotentHint {
		t.Fatalf("preview_publish annotations = %#v", tool.Annotations)
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
	if _, exists := properties["environment_routes"]; exists {
		t.Fatal("unused environment_routes remains public")
	}
	for name, raw := range properties {
		property := raw.(map[string]any)
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("preview_publish property %s has no description", name)
		}
	}
	branches := input["oneOf"].([]any)
	if len(branches) != 2 {
		t.Fatalf("preview_publish branches = %#v", branches)
	}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		if !required["action"] || !required["request_id"] {
			t.Fatalf("%s required = %#v", action, required)
		}
		switch action {
		case "server":
			if !required["port"] {
				t.Fatal("server does not require port")
			}
			if _, exists := branchProperties["routes"]; exists {
				t.Fatal("server accepts routes")
			}
		case "stack":
			if !required["routes"] {
				t.Fatal("stack does not require routes")
			}
			if _, exists := branchProperties["port"]; exists {
				t.Fatal("stack accepts port")
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
	if output["additionalProperties"] != false {
		t.Fatalf("preview_publish output remains open: %#v", output)
	}
	outputProperties := output["properties"].(map[string]any)
	if _, exists := outputProperties["request_id"]; !exists {
		t.Fatal("preview_publish output omits request_id")
	}

	operations, ok := tool.Meta["loki/operations"].(map[string]any)
	if !ok || len(operations) != 2 {
		t.Fatalf("preview_publish operations = %#v", tool.Meta)
	}
	for _, action := range []string{"server", "stack"} {
		semantics := operations[action].(map[string]any)
		if semantics["replay"] != string(ReplayRequestID) ||
			semantics["request_id_field"] != "request_id" ||
			semantics["failure_atomicity"] != string(FailureSingleResource) ||
			semantics["crash_recovery"] != string(CrashRecoveryNone) {
			t.Fatalf("%s semantics = %#v", action, semantics)
		}
	}
}
