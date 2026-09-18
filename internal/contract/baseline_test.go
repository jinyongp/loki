package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCurrentContract(t *testing.T) {
	snapshot, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Baseline != "go-0.49.0-dev" || len(snapshot.Tools) != 28 || len(snapshot.Resources) != 3 || len(snapshot.ResourceContents) != 3 {
		t.Fatal("incomplete current contract")
	}
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		seen[definition.Name] = true
	}
	for _, required := range []string{"system_inspect", "browser_session", "workspace_edit", "secret_write", "github", "github_issue_fields", "project_coordination", "project_coordination_write"} {
		if !seen[required] {
			t.Errorf("missing Loki tool %q", required)
		}
	}
	for _, delegated := range []string{"runtime_stop", "project", "task_inspect", "task_write", "task_delete", "bootstrap_project", "action", "command_run", "command_start", "process_inspect", "agent_context", "skill_read", "skill_write"} {
		if seen[delegated] {
			t.Errorf("delegated command remains exposed as MCP tool %q", delegated)
		}
	}
	for _, name := range []string{"project_coordination", "project_coordination_write"} {
		forbidden := map[string]bool{"context": true, "session_id": true, "profile": true, "command": true}
		var schema map[string]any
		encoded, err := json.Marshal(seenDefinition(definitions, name).InputSchema)
		if err != nil || json.Unmarshal(encoded, &schema) != nil {
			t.Fatalf("decode %s schema: %v", name, err)
		}
		properties, _ := schema["properties"].(map[string]any)
		for key := range forbidden {
			if _, exists := properties[key]; exists {
				t.Errorf("%s exposes forbidden field %q", name, key)
			}
		}
	}
	write := seenDefinition(definitions, "project_coordination_write")
	encodedWrite, err := json.Marshal(write.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var writeSchema map[string]any
	if json.Unmarshal(encodedWrite, &writeSchema) != nil {
		t.Fatal("invalid project_coordination_write schema")
	}
	writeProperties := writeSchema["properties"].(map[string]any)
	for _, key := range []string{"compaction_fingerprint", "compaction_through"} {
		if _, ok := writeProperties[key]; !ok {
			t.Errorf("project_coordination_write missing %q", key)
		}
	}
	data, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"process_log", "materialization", "action_name"} {
		if strings.Contains(string(data), legacy) {
			t.Errorf("legacy option remains in current tools: %s", legacy)
		}
	}
}

func TestCurrentContractContainsNoCredentialMaterial(t *testing.T) {
	lower := strings.ToLower(string(currentJSON))
	for _, forbidden := range []string{
		"authorization: bearer",
		"-----begin private key-----",
		"-----begin openssh private key-----",
		"tunnel_token=",
	} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("current contract contains forbidden credential marker %q", forbidden)
		}
	}
}

func TestCurrentReturnsIndependentCopies(t *testing.T) {
	first, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	first.Tools[0][0] = 'x'
	second, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if second.Tools[0][0] == 'x' {
		t.Fatal("current contract returned shared mutable data")
	}
}

func seenDefinition(definitions []*mcp.Tool, name string) *mcp.Tool {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition
		}
	}
	return nil
}
