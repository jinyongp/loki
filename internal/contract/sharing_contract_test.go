package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSharedResourcesAndRevokeContracts(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	shared := seenDefinition(definitions, "shared_resources")
	revoke := seenDefinition(definitions, "revoke_share")
	if shared == nil || revoke == nil {
		t.Fatalf("share contracts missing: shared=%#v revoke=%#v", shared, revoke)
	}

	encoded, err := json.Marshal(shared.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var sharedInput map[string]any
	if err := json.Unmarshal(encoded, &sharedInput); err != nil {
		t.Fatal(err)
	}
	if _, required := sharedInput["required"]; required {
		t.Fatalf("shared_resources unexpectedly requires kind: %#v", sharedInput)
	}
	kind := sharedInput["properties"].(map[string]any)["kind"].(map[string]any)
	if kind["default"] != "all" || strings.TrimSpace(kind["description"].(string)) == "" {
		t.Fatalf("shared_resources kind = %#v", kind)
	}
	encoded, err = json.Marshal(shared.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var sharedOutput map[string]any
	if err := json.Unmarshal(encoded, &sharedOutput); err != nil {
		t.Fatal(err)
	}
	branches := sharedOutput["oneOf"].([]any)
	if len(branches) != 3 {
		t.Fatalf("shared_resources outputs = %#v", branches)
	}
	for _, raw := range branches {
		if raw.(map[string]any)["additionalProperties"] != false {
			t.Fatalf("shared_resources output is open: %#v", raw)
		}
	}

	if revoke.Annotations == nil || !revoke.Annotations.IdempotentHint {
		t.Fatalf("revoke annotations = %#v", revoke.Annotations)
	}
	encoded, err = json.Marshal(revoke.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var revokeInput map[string]any
	if err := json.Unmarshal(encoded, &revokeInput); err != nil {
		t.Fatal(err)
	}
	properties := revokeInput["properties"].(map[string]any)
	for name, raw := range properties {
		if description, _ := raw.(map[string]any)["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("revoke_share property %s has no description", name)
		}
	}
	if branches := revokeInput["oneOf"].([]any); len(branches) != 2 {
		t.Fatalf("revoke_share input branches = %#v", branches)
	}
	encoded, err = json.Marshal(revoke.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var revokeOutput map[string]any
	if err := json.Unmarshal(encoded, &revokeOutput); err != nil {
		t.Fatal(err)
	}
	for _, raw := range revokeOutput["oneOf"].([]any) {
		if raw.(map[string]any)["additionalProperties"] != false {
			t.Fatalf("revoke_share output is open: %#v", raw)
		}
	}
	operations, ok := revoke.Meta["loki/operations"].(map[string]any)
	if !ok || len(operations) != 1 {
		t.Fatalf("revoke_share operation metadata = %#v", revoke.Meta)
	}
	semantics := operations["revoke"].(map[string]any)
	if semantics["replay"] != string(ReplayIdempotent) ||
		semantics["failure_atomicity"] != string(FailureSingleResource) ||
		semantics["crash_recovery"] != string(CrashRecoveryNone) {
		t.Fatalf("revoke semantics = %#v", semantics)
	}
}
