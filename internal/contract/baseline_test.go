package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type baseline struct {
	Baseline          string           `json:"baseline"`
	Initialize        map[string]any   `json:"initialize"`
	Tools             []map[string]any `json:"tools"`
	Resources         []map[string]any `json:"resources"`
	ResourceTemplates []map[string]any `json:"resourceTemplates"`
}

func loadBaseline(t *testing.T) baseline {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "contracts", "mcp-v046.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value baseline
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPython046Baseline(t *testing.T) {
	value := loadBaseline(t)
	if value.Baseline != "python-0.46.0" {
		t.Fatalf("baseline = %q", value.Baseline)
	}
	if len(value.Tools) != 34 {
		t.Fatalf("tool count = %d", len(value.Tools))
	}
	seen := make(map[string]bool, len(value.Tools))
	for _, tool := range value.Tools {
		name, ok := tool["name"].(string)
		if !ok || name == "" {
			t.Fatalf("tool has invalid name: %#v", tool)
		}
		if seen[name] {
			t.Fatalf("duplicate tool %q", name)
		}
		seen[name] = true
	}
	for _, required := range []string{"project", "action", "secret_write", "browser_session", "preview_publish"} {
		if !seen[required] {
			t.Errorf("missing tool %q", required)
		}
	}
}

func TestBaselineContainsNoCredentialMaterial(t *testing.T) {
	for _, path := range []string{filepath.Join("..", "..", "testdata", "contracts", "mcp-v046.json"), filepath.Join("testdata", "mcp-v0471.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(data))
		for _, forbidden := range []string{
			"authorization: bearer",
			"-----begin private key-----",
			"-----begin openssh private key-----",
			"tunnel_token=",
		} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("%s contains forbidden credential marker %q", path, forbidden)
			}
		}
	}
}

func TestCurrentBaseline(t *testing.T) {
	value, err := Baseline()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := value.Definitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 37 || len(value.Resources) != 3 || len(value.ResourceContents) != 3 {
		t.Fatal("incomplete current contract")
	}
	for _, name := range []string{"task_inspect", "task_write", "task_delete"} {
		found := false
		for _, tool := range tools {
			if tool.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s", name)
		}
	}
}

func TestCurrentDefinitionsContainOnlyLokiTools(t *testing.T) {
	tools, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 24 {
		t.Fatalf("tool count = %d", len(tools))
	}
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		seen[tool.Name] = true
	}
	for _, required := range []string{"system_inspect", "browser_session", "workspace_edit", "secret_write"} {
		if !seen[required] {
			t.Errorf("missing Loki tool %q", required)
		}
	}
	for _, delegated := range []string{"runtime_stop", "project", "task_inspect", "task_write", "task_delete", "bootstrap_project", "action", "command_run", "command_start", "process_inspect", "agent_context", "skill_read", "skill_write"} {
		if seen[delegated] {
			t.Errorf("devtools command remains exposed as MCP tool %q", delegated)
		}
	}
	data, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"process_log", "materialization", "action_name"} {
		if strings.Contains(string(data), legacy) {
			t.Errorf("legacy option remains in current tools: %s", legacy)
		}
	}
}
