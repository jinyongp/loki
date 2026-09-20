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
		"description": "Monotonic browser lifecycle/page generation. It advances on browser start/stop and before each validated navigation attempt so earlier observations are conservatively invalidated.",
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
			{Name: "expected_browser_generation", Schema: browserExpectedGenerationSchema(
				"Browser generation that back/forward/reload/stop_loading is based on. Stale generations fail before the navigation command.",
			)},
		},
		Variants: []ActionVariant{
			{Name: "start"},
			{Name: "navigate", Required: []string{"url"}, Optional: []string{"new_tab"}},
			{Name: "back", Required: []string{"expected_browser_generation"}},
			{Name: "forward", Required: []string{"expected_browser_generation"}},
			{Name: "reload", Required: []string{"expected_browser_generation"}},
			{Name: "stop_loading", Required: []string{"expected_browser_generation"}},
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
	historyNavigation := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"navigation":         map[string]any{"type": "string", "enum": []string{"back", "forward", "reload", "stop_loading"}},
			"performed":          map[string]any{"type": "boolean"},
			"url":                map[string]any{"type": "string"},
			"title":              map[string]any{"type": "string"},
			"active_tab_id":      browserTabIDSchema(),
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"navigation", "performed", "url", "title", "active_tab_id", "browser_generation"},
	}
	stopped := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"status":             map[string]any{"const": "stopped"},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"status", "browser_generation"},
	}

	tool.Description = "Control browser lifecycle and navigation with action-specific inputs. back, forward, reload, and stop_loading are generation-guarded session operations; history no-ops return performed=false without advancing browser_generation."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{
		"type":  "object",
		"oneOf": []any{running, navigated, historyNavigation, stopped},
	}
	guardedNavigation := func() OperationSemantics {
		return OperationSemantics{
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=state",
		}
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
		"back":         guardedNavigation(),
		"forward":      guardedNavigation(),
		"reload":       guardedNavigation(),
		"stop_loading": guardedNavigation(),
		"stop": {
			Replay: ReplayIdempotent, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryNone,
			AffectedResourceLimit: 1, RecoveryReference: "repeat browser_session action=stop",
		},
	})
}
