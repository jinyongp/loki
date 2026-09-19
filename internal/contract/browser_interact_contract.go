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
	tabID := browserTabIDSchema()

	rootProperties := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        []string{"click", "hover", "drag", "wheel", "type", "press", "back", "switch_tab", "close_tab"},
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
		browserInteractBranch("type", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "expected_state_generation": clone(expectedState),
			"index": clone(index), "text": clone(text),
		}, "expected_browser_generation", "expected_state_generation", "index", "text"),
		browserInteractBranch("press", map[string]any{
			"expected_browser_generation": clone(expectedBrowser), "key": clone(key),
		}, "expected_browser_generation", "key"),
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
	typeResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"typed": map[string]any{"const": true}, "index": map[string]any{"type": "integer", "minimum": 0},
			"characters": map[string]any{"type": "integer", "minimum": 0}, "browser_generation": browserGenerationSchema(),
		},
		"required": []string{"typed", "index", "characters", "browser_generation"},
	}
	pressResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"pressed": map[string]any{"type": "string"}, "browser_generation": browserGenerationSchema()},
		"required":   []string{"pressed", "browser_generation"},
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

	tool.Description = "Interact with the active desktop browser using expected_browser_generation. Pointer actions support guarded click, hover, drag, and real wheel events; element-index actions additionally require expected_state_generation. Stale references fail as conflicts before side effects."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "browser_interactArguments", "additionalProperties": false,
		"properties": rootProperties, "required": []string{"action"}, "oneOf": branches,
	}
	tool.OutputSchema = map[string]any{
		"type":  "object",
		"oneOf": []any{clickResult, hoverResult, dragResult, wheelResult, typeResult, pressResult, backResult, switchResult, closeResult},
	}
	guarded := func(reference string) OperationSemantics {
		return OperationSemantics{
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: reference,
		}
	}
	operations := map[string]OperationSemantics{
		"click":      guarded("browser_observe action=state"),
		"hover":      guarded("browser_observe action=state"),
		"drag":       guarded("browser_observe action=state"),
		"wheel":      guarded("browser_observe action=state"),
		"press":      guarded("browser_observe action=state"),
		"back":       guarded("browser_observe action=state"),
		"switch_tab": guarded("browser_observe action=tabs"),
		"close_tab":  guarded("browser_observe action=tabs"),
	}
	operations["type"] = OperationSemantics{
		Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_browser_generation", "expected_state_generation"},
		FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
		AffectedResourceLimit: 1, RecoveryReference: "browser_observe action=state",
	}
	return ApplyOperationMetadata(tool, operations)
}
