package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func browserExpectedGenerationSchema(description string) map[string]any {
	return map[string]any{
		"type": "integer", "minimum": 0, "maximum": 9007199254740991,
		"description": description,
	}
}

func browserInteractBranch(action string, properties map[string]any, required ...string) map[string]any {
	all := map[string]any{
		"action": map[string]any{
			"type": "string", "const": action,
			"description": "Browser interaction to perform.",
		},
	}
	for name, schema := range properties {
		all[name] = schema
	}
	fields := []string{"action"}
	fields = append(fields, required...)
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": all, "required": fields,
	}
}

func overrideBrowserInteract(tool *mcp.Tool) error {
	expectedBrowser := browserExpectedGenerationSchema("Browser generation returned by the observation/session result this interaction is based on. Stale generations fail as conflicts.")
	expectedState := browserExpectedGenerationSchema("State generation returned by browser_observe action=state. Required when an interaction references an element index.")
	index := map[string]any{
		"type": "integer", "minimum": 0, "maximum": 1000000,
		"description": "Interactive element index returned by browser_observe action=state.",
	}
	text := map[string]any{
		"type": "string", "maxLength": 65536,
		"description": "Text to replace the selected editable element content with; may be empty.",
	}
	key := map[string]any{
		"type": "string",
		"enum": []string{
			"Enter", "Tab", "Escape", "Backspace", "Delete",
			"ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
			"PageUp", "PageDown", "Home", "End", "Space",
		},
		"description": "Supported browser key to press on the active page.",
	}
	direction := map[string]any{
		"type": "string", "enum": []string{"up", "down"}, "default": "down",
		"description": "Vertical scroll direction.",
	}
	amount := map[string]any{
		"type": "integer", "minimum": 1, "maximum": 10000, "default": 500,
		"description": "Vertical scroll distance in CSS pixels.",
	}
	x := map[string]any{
		"type": "integer", "minimum": 0, "maximum": 16384,
		"description": "Viewport X coordinate for coordinate-based click.",
	}
	y := map[string]any{
		"type": "integer", "minimum": 0, "maximum": 16384,
		"description": "Viewport Y coordinate for coordinate-based click.",
	}
	newTab := map[string]any{
		"type": "boolean", "default": false,
		"description": "For element-index click, open an element href in a new tab when available.",
	}
	tabID := browserTabIDSchema()

	rootProperties := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        []string{"click", "type", "press", "scroll", "back", "switch_tab", "close_tab"},
			"description": "Browser interaction to perform.",
		},
		"expected_browser_generation": expectedBrowser,
		"expected_state_generation":   expectedState,
		"index":                       index,
		"text":                        text,
		"key":                         key,
		"direction":                   direction,
		"amount":                      amount,
		"x":                           x,
		"y":                           y,
		"new_tab":                     newTab,
		"tab_id":                      tabID,
	}

	clone := func(value map[string]any) map[string]any {
		result := make(map[string]any, len(value))
		for k, v := range value {
			result[k] = v
		}
		return result
	}
	branches := []any{
		browserInteractBranch("click", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"expected_state_generation":   clone(expectedState),
			"index":                       clone(index),
			"new_tab":                     clone(newTab),
		}, "expected_browser_generation", "expected_state_generation", "index"),
		browserInteractBranch("click", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"x":                           clone(x), "y": clone(y),
		}, "expected_browser_generation", "x", "y"),
		browserInteractBranch("type", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"expected_state_generation":   clone(expectedState),
			"index":                       clone(index), "text": clone(text),
		}, "expected_browser_generation", "expected_state_generation", "index", "text"),
		browserInteractBranch("press", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"key":                         clone(key),
		}, "expected_browser_generation", "key"),
		browserInteractBranch("scroll", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"direction":                   clone(direction), "amount": clone(amount),
		}, "expected_browser_generation"),
		browserInteractBranch("back", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
		}, "expected_browser_generation"),
		browserInteractBranch("switch_tab", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"tab_id":                      clone(tabID),
		}, "expected_browser_generation", "tab_id"),
		browserInteractBranch("close_tab", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"tab_id":                      clone(tabID),
		}, "expected_browser_generation", "tab_id"),
	}

	clickedElement := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"index": map[string]any{"type": "integer", "minimum": 0}},
		"required":   []string{"index"},
	}
	clickedCoordinates := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"x": map[string]any{"type": "integer", "minimum": 0},
			"y": map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{"x", "y"},
	}
	clickResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"clicked":            map[string]any{"oneOf": []any{clickedElement, clickedCoordinates}},
			"new_tab":            map[string]any{"type": "boolean"},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"clicked", "new_tab", "browser_generation"},
	}
	typeResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"typed":              map[string]any{"const": true},
			"index":              map[string]any{"type": "integer", "minimum": 0},
			"characters":         map[string]any{"type": "integer", "minimum": 0},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"typed", "index", "characters", "browser_generation"},
	}
	pressResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"pressed":            map[string]any{"type": "string"},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"pressed", "browser_generation"},
	}
	scrollResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"direction":          map[string]any{"type": "string", "enum": []string{"up", "down"}},
			"amount":             map[string]any{"type": "integer", "minimum": 1},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"direction", "amount", "browser_generation"},
	}
	backResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"url":                map[string]any{"type": "string"},
			"title":              map[string]any{"type": "string"},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"url", "title", "browser_generation"},
	}
	switchResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"url":                map[string]any{"type": "string"},
			"title":              map[string]any{"type": "string"},
			"tab_id":             browserTabIDSchema(),
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"url", "title", "tab_id", "browser_generation"},
	}
	closeResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"closed": browserTabIDSchema(),
			"active_tab_id": map[string]any{
				"anyOf": []any{browserTabIDSchema(), map[string]any{"type": "null"}},
			},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"closed", "active_tab_id", "browser_generation"},
	}

	tool.Description = "Interact with the active browser using generation preconditions. All actions require expected_browser_generation; element-index click/type additionally require expected_state_generation from browser_observe action=state. Stale references fail as conflicts before side effects."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "browser_interactArguments",
		"additionalProperties": false,
		"properties":           rootProperties,
		"required":             []string{"action"},
		"oneOf":                branches,
	}
	tool.OutputSchema = map[string]any{
		"type":  "object",
		"oneOf": []any{clickResult, typeResult, pressResult, scrollResult, backResult, switchResult, closeResult},
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"click": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=state",
		},
		"type": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation", "expected_state_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=state",
		},
		"press": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=state",
		},
		"scroll": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=state",
		},
		"back": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=state",
		},
		"switch_tab": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=tabs",
		},
		"close_tab": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=tabs",
		},
	})
}
