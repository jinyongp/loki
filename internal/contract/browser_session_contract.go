package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func browserTabIDSchema() map[string]any {
	return map[string]any{
		"type": "string", "pattern": "^[0-9A-Fa-f]{4}$",
		"description": "Four-character active tab identifier returned by browser session or observation results.",
	}
}

func browserGenerationSchema() map[string]any {
	return map[string]any{
		"type": "integer", "minimum": 0,
		"description": "Monotonic browser lifecycle/page generation. It increases when the browser starts, stops, or completes navigation.",
	}
}

func overrideBrowserSession(tool *mcp.Tool) error {
	input, err := (ActionInputContract{
		Title:             "browser_sessionArguments",
		ActionDescription: "Browser lifecycle or navigation operation.",
		Fields: []ActionField{
			{Name: "url", Schema: map[string]any{
				"type": "string", "minLength": 1,
				"description": "HTTP(S) URL to navigate the active tab to; valid only for action=navigate.",
			}},
			{Name: "new_tab", Schema: map[string]any{
				"type": "boolean", "default": false,
				"description": "Open the navigation in a new browser tab instead of the active tab; valid only for action=navigate.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "start"},
			{Name: "navigate", Required: []string{"url"}, Optional: []string{"new_tab"}},
			{Name: "stop"},
		},
	}).Schema()
	if err != nil {
		return err
	}

	running := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"status":             map[string]any{"const": "running"},
			"active_tab_id":      browserTabIDSchema(),
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"status", "active_tab_id", "browser_generation"},
	}
	navigated := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"url":                map[string]any{"type": "string"},
			"title":              map[string]any{"type": "string"},
			"new_tab":            map[string]any{"type": "boolean"},
			"active_tab_id":      browserTabIDSchema(),
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"url", "title", "new_tab", "active_tab_id", "browser_generation"},
	}
	stopped := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"status":             map[string]any{"const": "stopped"},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"status", "browser_generation"},
	}

	tool.Description = "Control browser lifecycle and navigation with action-specific inputs. Results expose browser_generation so later observations and interactions can identify the browser/page generation they belong to."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{
		"type":  "object",
		"oneOf": []any{running, navigated, stopped},
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"start": {
			Replay: ReplayIdempotent, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=state",
		},
		"navigate": {
			Replay: ReplayUnsafe, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=state",
		},
		"stop": {
			Replay: ReplayIdempotent, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryNone,
			AffectedResourceLimit: 1, RecoveryReference: "repeat browser_session action=stop",
		},
	})
}
