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
	coordinate := func(description string) map[string]any {
		return map[string]any{
			"type": "integer", "minimum": 0, "maximum": 16384,
			"description": description,
		}
	}
	x := coordinate("Viewport X coordinate for a pointer target.")
	y := coordinate("Viewport Y coordinate for a pointer target.")
	button := map[string]any{
		"type": "string", "enum": []string{"left", "middle", "right"}, "default": "left",
		"description": "Mouse button used by click.",
	}
	clickCount := map[string]any{
		"type": "integer", "minimum": 1, "maximum": 3, "default": 1,
		"description": "Click count sent for the mouse gesture; 2 produces a double-click and 3 a triple-click.",
	}
	modifiers := map[string]any{
		"type": "array", "maxItems": 4, "uniqueItems": true,
		"items":       map[string]any{"type": "string", "enum": []string{"Alt", "Control", "Meta", "Shift"}},
		"description": "Modifier keys held during the click gesture.",
	}
	newTab := map[string]any{
		"type": "boolean", "const": true,
		"description": "Open an indexed link href in a new browser tab; this dedicated branch cannot be combined with button/count/modifier overrides.",
	}
	sourceIndex := map[string]any{
		"type": "integer", "minimum": 0, "maximum": 1000000,
		"description": "Source element index for a drag gesture.",
	}
	targetIndex := map[string]any{
		"type": "integer", "minimum": 0, "maximum": 1000000,
		"description": "Visible target element index for an element-to-element drag gesture.",
	}
	steps := map[string]any{
		"type": "integer", "minimum": 1, "maximum": 100, "default": 12,
		"description": "Number of interpolated mouse-move events emitted while the left button is held.",
	}
	duration := map[string]any{
		"type": "integer", "minimum": 0, "maximum": 5000, "default": 250,
		"description": "Approximate drag duration in milliseconds, distributed across the bounded interpolation steps.",
	}
	delta := func(axis string) map[string]any {
		return map[string]any{
			"type": "integer", "minimum": -10000, "maximum": 10000,
			"description": "Wheel delta on the " + axis + " axis in CSS-pixel-like units; delta_x and delta_y cannot both be zero.",
		}
	}
	text := map[string]any{
		"type": "string", "maxLength": 65536,
		"description": "Text used by fill or type; may be empty.",
	}
	key := map[string]any{
		"type": "string",
		"anyOf": []any{
			map[string]any{"enum": []string{
				"Enter", "Tab", "Escape", "Backspace", "Delete", "Insert",
				"ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
				"PageUp", "PageDown", "Home", "End", "Space",
				"F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11", "F12",
			}},
			map[string]any{"pattern": "^[A-Za-z0-9]$"},
		},
		"description": "Supported named browser key or one ASCII letter/digit; letters are normalized to uppercase in results.",
	}
	optionSelector := map[string]any{
		"oneOf": []any{
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"value": map[string]any{"type": "string", "maxLength": 500}},
				"required":   []string{"value"},
			},
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"label": map[string]any{"type": "string", "maxLength": 500}},
				"required":   []string{"label"},
			},
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"index": map[string]any{"type": "integer", "minimum": 0, "maximum": 100000}},
				"required":   []string{"index"},
			},
		},
	}
	options := map[string]any{
		"type": "array", "minItems": 1, "maxItems": 50, "items": optionSelector,
		"description": "Options to select, each identified by exactly one value, label, or option index; duplicate/ambiguous matches are resolved before mutation.",
	}
	checked := map[string]any{
		"type":        "boolean",
		"description": "Desired checked state for a native checkbox or radio input.",
	}
	tabID := browserTabIDSchema()

	rootProperties := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        []string{"click", "hover", "drag", "wheel", "fill", "type", "key", "shortcut", "select_option", "set_checked", "focus", "back", "switch_tab", "close_tab"},
			"description": "Browser interaction to perform.",
		},
		"expected_browser_generation": expectedBrowser,
		"expected_state_generation":   expectedState,
		"index":                       index,
		"x":                           x,
		"y":                           y,
		"button":                      button,
		"click_count":                 clickCount,
		"modifiers":                   modifiers,
		"new_tab":                     newTab,
		"source_index":                sourceIndex,
		"target_index":                targetIndex,
		"from_x":                      coordinate("Viewport X coordinate where a coordinate drag starts."),
		"from_y":                      coordinate("Viewport Y coordinate where a coordinate drag starts."),
		"to_x":                        coordinate("Viewport X coordinate where a drag ends."),
		"to_y":                        coordinate("Viewport Y coordinate where a drag ends."),
		"steps":                       steps,
		"duration_ms":                 duration,
		"delta_x":                     delta("horizontal"),
		"delta_y":                     delta("vertical"),
		"text":                        text,
		"key":                         key,
		"options":                     options,
		"checked":                     checked,
		"tab_id":                      tabID,
	}

	clone := func(value map[string]any) map[string]any {
		result := make(map[string]any, len(value))
		for k, v := range value {
			result[k] = v
		}
		return result
	}
	shortcutModifiers := clone(modifiers)
	shortcutModifiers["minItems"] = 1
	branches := []any{
		browserInteractBranch("click", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"expected_state_generation":   clone(expectedState),
			"index":                       clone(index), "button": clone(button), "click_count": clone(clickCount), "modifiers": clone(modifiers),
		}, "expected_browser_generation", "expected_state_generation", "index"),
		browserInteractBranch("click", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"expected_state_generation":   clone(expectedState),
			"index":                       clone(index), "new_tab": clone(newTab),
		}, "expected_browser_generation", "expected_state_generation", "index", "new_tab"),
		browserInteractBranch("click", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"x":                           clone(x), "y": clone(y), "button": clone(button), "click_count": clone(clickCount), "modifiers": clone(modifiers),
		}, "expected_browser_generation", "x", "y"),
		browserInteractBranch("hover", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"expected_state_generation":   clone(expectedState), "index": clone(index),
		}, "expected_browser_generation", "expected_state_generation", "index"),
		browserInteractBranch("hover", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "x": clone(x), "y": clone(y),
		}, "expected_browser_generation", "x", "y"),
		browserInteractBranch("drag", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "expected_state_generation": clone(expectedState),
			"source_index": clone(sourceIndex), "target_index": clone(targetIndex), "steps": clone(steps), "duration_ms": clone(duration),
		}, "expected_browser_generation", "expected_state_generation", "source_index", "target_index"),
		browserInteractBranch("drag", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "expected_state_generation": clone(expectedState),
			"source_index": clone(sourceIndex), "to_x": clone(rootProperties["to_x"].(map[string]any)), "to_y": clone(rootProperties["to_y"].(map[string]any)),
			"steps": clone(steps), "duration_ms": clone(duration),
		}, "expected_browser_generation", "expected_state_generation", "source_index", "to_x", "to_y"),
		browserInteractBranch("drag", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
			"from_x":                      clone(rootProperties["from_x"].(map[string]any)), "from_y": clone(rootProperties["from_y"].(map[string]any)),
			"to_x": clone(rootProperties["to_x"].(map[string]any)), "to_y": clone(rootProperties["to_y"].(map[string]any)),
			"steps": clone(steps), "duration_ms": clone(duration),
		}, "expected_browser_generation", "from_x", "from_y", "to_x", "to_y"),
		browserInteractBranch("wheel", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "delta_x": clone(rootProperties["delta_x"].(map[string]any)), "delta_y": clone(rootProperties["delta_y"].(map[string]any)),
		}, "expected_browser_generation", "delta_x", "delta_y"),
		browserInteractBranch("wheel", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "expected_state_generation": clone(expectedState), "index": clone(index),
			"delta_x": clone(rootProperties["delta_x"].(map[string]any)), "delta_y": clone(rootProperties["delta_y"].(map[string]any)),
		}, "expected_browser_generation", "expected_state_generation", "index", "delta_x", "delta_y"),
		browserInteractBranch("wheel", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "x": clone(x), "y": clone(y),
			"delta_x": clone(rootProperties["delta_x"].(map[string]any)), "delta_y": clone(rootProperties["delta_y"].(map[string]any)),
		}, "expected_browser_generation", "x", "y", "delta_x", "delta_y"),
		browserInteractBranch("fill", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "expected_state_generation": clone(expectedState),
			"index": clone(index), "text": clone(text),
		}, "expected_browser_generation", "expected_state_generation", "index", "text"),
		browserInteractBranch("type", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "expected_state_generation": clone(expectedState),
			"index": clone(index), "text": clone(text),
		}, "expected_browser_generation", "expected_state_generation", "index", "text"),
		browserInteractBranch("key", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "key": clone(key), "modifiers": clone(modifiers),
		}, "expected_browser_generation", "key"),
		browserInteractBranch("shortcut", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "key": clone(key), "modifiers": shortcutModifiers,
		}, "expected_browser_generation", "key", "modifiers"),
		browserInteractBranch("select_option", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "expected_state_generation": clone(expectedState),
			"index": clone(index), "options": clone(options),
		}, "expected_browser_generation", "expected_state_generation", "index", "options"),
		browserInteractBranch("set_checked", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "expected_state_generation": clone(expectedState),
			"index": clone(index), "checked": clone(checked),
		}, "expected_browser_generation", "expected_state_generation", "index", "checked"),
		browserInteractBranch("focus", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "expected_state_generation": clone(expectedState),
			"index": clone(index),
		}, "expected_browser_generation", "expected_state_generation", "index"),
		browserInteractBranch("back", map[string]any{
			"expected_browser_generation": clone(expectedBrowser),
		}, "expected_browser_generation"),
		browserInteractBranch("switch_tab", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "tab_id": clone(tabID),
		}, "expected_browser_generation", "tab_id"),
		browserInteractBranch("close_tab", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "tab_id": clone(tabID),
		}, "expected_browser_generation", "tab_id"),
	}

	elementTarget := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"index": map[string]any{"type": "integer", "minimum": 0}},
		"required":   []string{"index"},
	}
	coordinateTarget := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"x": map[string]any{"type": "number"}, "y": map[string]any{"type": "number"},
		},
		"required": []string{"x", "y"},
	}
	clickResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"clicked":            map[string]any{"oneOf": []any{elementTarget, coordinateTarget}},
			"button":             map[string]any{"type": "string", "enum": []string{"left", "middle", "right"}},
			"click_count":        map[string]any{"type": "integer", "minimum": 1, "maximum": 3},
			"modifiers":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"Alt", "Control", "Meta", "Shift"}}},
			"new_tab":            map[string]any{"type": "boolean"},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"clicked", "button", "click_count", "modifiers", "new_tab", "browser_generation"},
	}
	hoverResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"hovered":            map[string]any{"oneOf": []any{elementTarget, coordinateTarget}},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"hovered", "browser_generation"},
	}
	dragged := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"from_x": map[string]any{"type": "number"}, "from_y": map[string]any{"type": "number"},
			"to_x": map[string]any{"type": "number"}, "to_y": map[string]any{"type": "number"},
			"source_index": map[string]any{"type": "integer", "minimum": 0},
			"target_index": map[string]any{"type": "integer", "minimum": 0},
			"steps":        map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			"duration_ms":  map[string]any{"type": "integer", "minimum": 0, "maximum": 5000},
		},
		"required": []string{"from_x", "from_y", "to_x", "to_y", "steps", "duration_ms"},
	}
	dragResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"dragged": dragged, "browser_generation": browserGenerationSchema(),
		},
		"required": []string{"dragged", "browser_generation"},
	}
	wheelDetail := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"x": map[string]any{"type": "number"}, "y": map[string]any{"type": "number"},
			"index":   map[string]any{"type": "integer", "minimum": 0},
			"delta_x": map[string]any{"type": "integer", "minimum": -10000, "maximum": 10000},
			"delta_y": map[string]any{"type": "integer", "minimum": -10000, "maximum": 10000},
		},
		"required": []string{"x", "y", "delta_x", "delta_y"},
	}
	wheelResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"wheel": wheelDetail, "browser_generation": browserGenerationSchema(),
		},
		"required": []string{"wheel", "browser_generation"},
	}
	fillResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"filled": map[string]any{"const": true}, "index": map[string]any{"type": "integer", "minimum": 0},
			"characters": map[string]any{"type": "integer", "minimum": 0}, "browser_generation": browserGenerationSchema(),
		},
		"required": []string{"filled", "index", "characters", "browser_generation"},
	}
	typeResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"typed": map[string]any{"const": true}, "index": map[string]any{"type": "integer", "minimum": 0},
			"characters": map[string]any{"type": "integer", "minimum": 0}, "browser_generation": browserGenerationSchema(),
		},
		"required": []string{"typed", "index", "characters", "browser_generation"},
	}
	keyResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"key":                map[string]any{"type": "string"},
			"modifiers":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"Alt", "Control", "Meta", "Shift"}}},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"key", "modifiers", "browser_generation"},
	}
	shortcutDetail := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"key":       map[string]any{"type": "string"},
			"modifiers": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "enum": []string{"Alt", "Control", "Meta", "Shift"}}},
		},
		"required": []string{"key", "modifiers"},
	}
	shortcutResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"shortcut": shortcutDetail, "browser_generation": browserGenerationSchema()},
		"required":   []string{"shortcut", "browser_generation"},
	}
	selectedOption := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"index": map[string]any{"type": "integer", "minimum": 0},
			"value": map[string]any{"type": "string"},
			"label": map[string]any{"type": "string"},
		},
		"required": []string{"index", "value", "label"},
	}
	selectResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"index":              map[string]any{"type": "integer", "minimum": 0},
			"multiple":           map[string]any{"type": "boolean"},
			"changed":            map[string]any{"type": "boolean"},
			"selected":           map[string]any{"type": "array", "items": selectedOption},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"index", "multiple", "changed", "selected", "browser_generation"},
	}
	checkedResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"index":              map[string]any{"type": "integer", "minimum": 0},
			"checked":            map[string]any{"type": "boolean"},
			"changed":            map[string]any{"type": "boolean"},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"index", "checked", "changed", "browser_generation"},
	}
	focusResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"focused":            map[string]any{"const": true},
			"index":              map[string]any{"type": "integer", "minimum": 0},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"focused", "index", "browser_generation"},
	}
	backResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"url": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "browser_generation": browserGenerationSchema(),
		},
		"required": []string{"url", "title", "browser_generation"},
	}
	switchResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"url": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
			"tab_id": browserTabIDSchema(), "browser_generation": browserGenerationSchema(),
		},
		"required": []string{"url", "title", "tab_id", "browser_generation"},
	}
	closeResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"closed":             browserTabIDSchema(),
			"active_tab_id":      map[string]any{"anyOf": []any{browserTabIDSchema(), map[string]any{"type": "null"}}},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"closed", "active_tab_id", "browser_generation"},
	}

	tool.Description = "Interact with the active desktop browser using expected_browser_generation. Pointer, editable, keyboard, and form actions use action-specific operands; every element-index action additionally requires expected_state_generation. fill replaces complete editable content while type preserves the current caret/selection. Stale references fail as conflicts before side effects."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "browser_interactArguments", "additionalProperties": false,
		"properties": rootProperties, "required": []string{"action"}, "oneOf": branches,
	}
	tool.OutputSchema = map[string]any{
		"type":  "object",
		"oneOf": []any{clickResult, hoverResult, dragResult, wheelResult, fillResult, typeResult, keyResult, shortcutResult, selectResult, checkedResult, focusResult, backResult, switchResult, closeResult},
	}
	guarded := func(reference string) OperationSemantics {
		return OperationSemantics{
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: reference,
		}
	}
	elementGuarded := func() OperationSemantics {
		return OperationSemantics{
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation", "expected_state_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=state",
		}
	}
	operations := map[string]OperationSemantics{
		"click":         guarded("browser_observe action=state"),
		"hover":         guarded("browser_observe action=state"),
		"drag":          guarded("browser_observe action=state"),
		"wheel":         guarded("browser_observe action=state"),
		"fill":          elementGuarded(),
		"type":          elementGuarded(),
		"key":           guarded("browser_observe action=state"),
		"shortcut":      guarded("browser_observe action=state"),
		"select_option": elementGuarded(),
		"set_checked":   elementGuarded(),
		"focus":         elementGuarded(),
		"back":          guarded("browser_observe action=state"),
		"switch_tab":    guarded("browser_observe action=tabs"),
		"close_tab":     guarded("browser_observe action=tabs"),
	}
	return ApplyOperationMetadata(tool, operations)
}
