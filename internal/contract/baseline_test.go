package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCurrentContract(t *testing.T) {
	snapshot, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Baseline != "go-0.48.0-dev" || len(snapshot.Tools) != 24 || len(snapshot.Resources) != 3 || len(snapshot.ResourceContents) != 3 {
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
	for _, required := range []string{"system_inspect", "browser_session", "workspace_edit", "secret_write"} {
		if !seen[required] {
			t.Errorf("missing Loki tool %q", required)
		}
	}
	for _, delegated := range []string{"runtime_stop", "project", "task_inspect", "task_write", "task_delete", "bootstrap_project", "action", "command_run", "command_start", "process_inspect", "agent_context", "skill_read", "skill_write"} {
		if seen[delegated] {
			t.Errorf("delegated command remains exposed as MCP tool %q", delegated)
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
