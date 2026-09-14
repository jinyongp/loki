package contract

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed testdata/mcp-v0471.json
var baselineJSON []byte

const BaselineVersion = "0.47.1"
const CatalogRevision = "2026-09-14.1"

const CurrentInstructions = `Operate inside the isolated Loki workspace. Before repository work, call agent_context for the intended cwd and activate the devtools skill when it is listed. Use the devtools CLI directly for project state, task queues, configured commands, checks, ports, and managed processes; inspect exact command contracts with devtools schema. Use Loki MCP tools for workspace and image access, browser control, previews and artifact sharing, Git operations, agent skills, and encrypted secret metadata. Keep active secret values in Loki's AES-GCM vault. For a configured process that needs those secrets, use loki secret-process start or restart with secret names; use ordinary devtools process commands for later status, readiness, logs, and stopping. Never place secret values in arguments, conversation, logs, devtools state, or workspace files.`

var currentToolNames = []string{
	"system_inspect",
	"preview_publish", "shared_resources", "revoke_share",
	"browser_session", "browser_observe", "browser_interact", "browser_screenshot", "browser_save_screenshot", "browser_share_screenshot",
	"workspace_read", "read_image", "share_image", "artifact_publish", "write_image", "workspace_edit",
	"restore_workspace_file", "remove_tracked_file",
	"agent_context", "skill_read", "skill_write",
	"git_inspect", "git_stage", "developer_view",
	"secret_inspect", "secret_write", "secret_delete",
}

// Baseline returns an independent copy; callers cannot mutate the canonical data.
func Baseline() (*Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(baselineJSON, &s); err != nil {
		return nil, err
	}
	if s.Baseline != "python-"+BaselineVersion || len(s.Tools) != 37 {
		return nil, fmt.Errorf("invalid embedded baseline")
	}
	return &s, nil
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
	snapshot, err := Baseline()
	if err != nil {
		return nil, err
	}
	all, err := snapshot.Definitions()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*mcp.Tool, len(all))
	for _, tool := range all {
		byName[tool.Name] = tool
	}
	selected := make([]*mcp.Tool, 0, len(currentToolNames))
	for _, name := range currentToolNames {
		tool := byName[name]
		if tool == nil {
			return nil, fmt.Errorf("current tool %s missing from baseline", name)
		}
		tool, err = currentTool(tool)
		if err != nil {
			return nil, err
		}
		selected = append(selected, tool)
	}
	return selected, nil
}

func currentTool(tool *mcp.Tool) (*mcp.Tool, error) {
	data, err := json.Marshal(tool)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	schema, _ := raw["inputSchema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	action, _ := properties["action"].(map[string]any)
	remove := func(names ...string) {
		for _, name := range names {
			delete(properties, name)
		}
	}
	switch tool.Name {
	case "preview_publish":
		action["enum"] = []any{"server", "stack"}
		remove("profile", "action_name", "cwd")
		raw["description"] = "Publish a live preview for one server or an arbitrary route stack."
	case "developer_view":
		action["enum"] = []any{"git_diff", "test_report"}
		remove("session_id", "offset", "limit")
		raw["description"] = "Render a Git diff or saved test report."
	case "secret_delete":
		action["enum"] = []any{"secret", "profile"}
		remove("action_name")
		raw["description"] = "Delete an encrypted secret or profile."
	}
	data, err = json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var current mcp.Tool
	if err = json.Unmarshal(data, &current); err != nil {
		return nil, err
	}
	return &current, nil
}
