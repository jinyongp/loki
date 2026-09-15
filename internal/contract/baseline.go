package contract

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed testdata/mcp-go-v048.json
var currentJSON []byte

const CatalogRevision = "2026-09-15.1"

const CurrentInstructions = `Operate inside the isolated Loki workspace. The devtools agent skill is preinstalled. Use the devtools CLI directly for project state, task queues, configured commands, checks, ports, and managed processes; inspect exact command contracts with devtools schema. Use Loki MCP tools for workspace and image access, browser control, previews and artifact sharing, Git operations, and encrypted secret metadata. Keep active secret values in Loki's AES-GCM vault. For a configured process that needs those secrets, use loki secret-process start or restart with secret names; use ordinary devtools process commands for later status, readiness, logs, and stopping. Never place secret values in arguments, conversation, logs, devtools state, or workspace files.`

var currentToolNames = []string{
	"system_inspect",
	"preview_publish", "shared_resources", "revoke_share",
	"browser_session", "browser_observe", "browser_interact", "browser_screenshot", "browser_save_screenshot", "browser_share_screenshot",
	"workspace_read", "read_image", "share_image", "artifact_publish", "write_image", "workspace_edit",
	"restore_workspace_file", "remove_tracked_file",
	"git_inspect", "git_stage", "developer_view",
	"secret_inspect", "secret_write", "secret_delete", "github_issue_fields",
}

// Current returns an independent copy; callers cannot mutate canonical data.
func Current() (*Snapshot, error) {
	var snapshot Snapshot
	if err := json.Unmarshal(currentJSON, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Baseline != "go-0.48.0-dev" || len(snapshot.Tools) != len(currentToolNames) {
		return nil, fmt.Errorf("invalid embedded Go contract")
	}
	return &snapshot, nil
}

// Snapshot retains raw JSON so SDK defaults cannot silently change the fixture.
type Snapshot struct {
	Baseline          string                     `json:"baseline"`
	Initialize        json.RawMessage            `json:"initialize"`
	Tools             []json.RawMessage          `json:"tools"`
	Resources         []json.RawMessage          `json:"resources"`
	ResourceTemplates []json.RawMessage          `json:"resourceTemplates"`
	ResourceContents  map[string]json.RawMessage `json:"resourceContents"`
}

func (s *Snapshot) Definitions() ([]*mcp.Tool, error) {
	items := make([]*mcp.Tool, 0, len(s.Tools))
	seen := map[string]bool{}
	for _, raw := range s.Tools {
		var t mcp.Tool
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		if t.Name == "" || seen[t.Name] {
			return nil, fmt.Errorf("invalid or duplicate tool name")
		}
		seen[t.Name] = true
		items = append(items, &t)
	}
	return items, nil
}

// CurrentDefinitions returns Loki-owned tools. Development workflow and process
// commands are provided by the pinned devtools CLI and its bundled agent skill.
func CurrentDefinitions() ([]*mcp.Tool, error) {
	snapshot, err := Current()
	if err != nil {
		return nil, err
	}
	definitions, err := snapshot.Definitions()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*mcp.Tool, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	selected := make([]*mcp.Tool, 0, len(currentToolNames))
	for index, name := range currentToolNames {
		definition := byName[name]
		if definition == nil {
			return nil, fmt.Errorf("current tool %q is missing at %d", name, index)
		}
		selected = append(selected, definition)
	}
	return selected, nil
}
