package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

// ProtectedJobTool is the active unified Job contract. Resource grants and
// private delivery authority are enforced by the broker/launcher boundary.
func ProtectedJobTool() (*mcp.Tool, error) {
	tool, err := jobTool()
	if err != nil {
		return nil, err
	}
	input := tool.InputSchema.(map[string]any)
	resource := map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$", "description": "Host-granted application resource alias. No profile or key-list authority is accepted."}
	env := map[string]any{"type": "string", "pattern": "^[a-zA-Z][a-zA-Z0-9_-]{0,63}$", "description": "Granted environment; omission selects common for a protected Job."}
	network := map[string]any{"type": "string", "enum": []string{"none", "dependency-install", "application"}, "default": "none", "description": "Logical network policy. application requires a granted resource and host-approved destinations."}
	properties := input["properties"].(map[string]any)
	properties["resource"], properties["env"], properties["network"] = resource, env, network
	for _, raw := range input["oneOf"].([]any) {
		branch := raw.(map[string]any)
		p := branch["properties"].(map[string]any)
		if p["action"].(map[string]any)["const"] != "start" {
			continue
		}
		p["resource"], p["env"], p["network"] = resource, env, network
		branch["allOf"] = []any{
			map[string]any{"if": map[string]any{"required": []string{"env"}}, "then": map[string]any{"required": []string{"resource"}}},
			map[string]any{"if": map[string]any{"properties": map[string]any{"network": map[string]any{"const": "application"}}, "required": []string{"network"}}, "then": map[string]any{"required": []string{"resource"}}},
			map[string]any{"if": map[string]any{"required": []string{"resource"}}, "then": map[string]any{"properties": map[string]any{"network": map[string]any{"enum": []string{"none", "application"}}, "request_id": map[string]any{"pattern": secretUUIDPattern}}}},
		}
	}
	gap := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"cursor":         map[string]any{"type": "integer", "minimum": 0, "maximum": 4194304},
		"reason":         map[string]any{"type": "string", "enum": []string{"collector_lost", "attach_lost", "masking_failed", "storage_failed", "recovery_interrupted"}},
		"unknown_length": map[string]any{"const": true},
	}, "required": []string{"cursor", "reason", "unknown_length"}}
	var extend func(map[string]any)
	extend = func(schema map[string]any) {
		p, ok := schema["properties"].(map[string]any)
		if !ok {
			return
		}
		if _, ok := p["outcome"]; ok {
			p["outcome"] = map[string]any{"type": "string", "enum": []string{"exited", "canceled", "timed_out", "oom_killed", "launch_failed", "outcome_unknown", "output_lost"}}
		}
		if _, ok := p["state"]; ok {
			p["output_complete"] = map[string]any{"type": "boolean", "description": "True only after both filtered streams reach trustworthy EOF; retention truncation is reported separately."}
			p["output_gap"] = gap
		}
		if status, ok := p["status"].(map[string]any); ok {
			extend(status)
		}
		if _, ok := p["network"]; ok {
			p["network"] = network
		}
	}
	for _, raw := range tool.OutputSchema.(map[string]any)["oneOf"].([]any) {
		extend(raw.(map[string]any))
	}
	return tool, nil
}
