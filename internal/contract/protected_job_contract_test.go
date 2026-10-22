package contract

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestProtectedJobContractRejectsExecutionAuthorityOverrides(t *testing.T) {
	tool, err := ProtectedJobTool()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"action": "start", "request_id": "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", "argv": []string{"/bin/true"}, "resource": "example", "env": "production", "network": "application"}
	if err = resolved.Validate(base); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"network": "dependency-install", "env": nil, "profile": "github-app", "keys": []string{"TOKEN"}, "values": map[string]string{"TOKEN": "private"}, "mount": "/", "uid": 0, "endpoint": "https://arbitrary", "request_id": "9AF6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2"} {
		input := map[string]any{}
		for k, v := range base {
			input[k] = v
		}
		input[key] = value
		if resolved.Validate(input) == nil {
			t.Fatalf("accepted %s=%v", key, value)
		}
	}
	delete(base, "resource")
	if resolved.Validate(base) == nil {
		t.Fatal("application network without resource accepted")
	}
	base["network"] = "none"
	if resolved.Validate(base) == nil {
		t.Fatal("env without resource accepted")
	}
	delete(base, "env")
	if err = resolved.Validate(base); err != nil {
		t.Fatal("ordinary Job rejected", err)
	}
}
