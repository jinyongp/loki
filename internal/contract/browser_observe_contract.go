package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func browserObservationSequenceProperties(itemName string, itemSchema map[string]any) map[string]any {
	return map[string]any{
		itemName:             map[string]any{"type": "array", "items": itemSchema},
		"latest_sequence":    map[string]any{"type": "integer", "minimum": 0},
		"oldest_sequence":    map[string]any{"type": "integer", "minimum": 0},
		"next_sequence":      map[string]any{"anyOf": []any{map[string]any{"type": "integer", "minimum": 0}, map[string]any{"type": "null"}}},
		"retained":           map[string]any{"type": "integer", "minimum": 0},
		"complete":           map[string]any{"type": "boolean"},
		"browser_generation": browserGenerationSchema(),
	}
}

func browserEventSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sequence":    map[string]any{"type": "integer", "minimum": 1},
			"captured_at": map[string]any{"type": "string"},
			"kind":        map[string]any{"type": "string"},
			"session_id":  map[string]any{"type": "string"},
		},
		"required":             []string{"sequence", "captured_at", "kind", "session_id"},
		"additionalProperties": true,
	}
}

func browserNetworkRequestSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"request_id":       map[string]any{"type": "string"},
			"session_id":       map[string]any{"type": "string"},
			"url":              map[string]any{"type": "string"},
			"method":           map[string]any{"type": "string"},
			"resource_type":    map[string]any{"type": "string"},
			"request_headers":  map[string]any{"type": "object", "additionalProperties": true},
			"status":           map[string]any{"anyOf": []any{map[string]any{"type": "number"}, map[string]any{"type": "null"}}},
			"status_text":      map[string]any{"type": "string"},
			"mime_type":        map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
			"protocol":         map[string]any{"type": "string"},
			"remote_ip":        map[string]any{"type": "string"},
			"from_disk_cache":  map[string]any{"type": "boolean"},
			"response_headers": map[string]any{"type": "object", "additionalProperties": true},
			"failed":           map[string]any{"type": "boolean"},
			"finished":         map[string]any{"type": "boolean"},
			"encoded_bytes":    map[string]any{"anyOf": []any{map[string]any{"type": "number"}, map[string]any{"type": "null"}}},
			"error":            map[string]any{"type": "string"},
			"blocked_reason":   map[string]any{"type": "string"},
			"canceled":         map[string]any{"type": "boolean"},
		},
		"required": []string{
			"request_id", "session_id", "url", "method", "resource_type", "request_headers",
			"status", "mime_type", "failed", "finished", "encoded_bytes",
		},
	}
}

func browserTabSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"tab_id": map[string]any{"type": "string", "pattern": "^[0-9A-Fa-f]{4}$"},
			"url":    map[string]any{"type": "string"},
			"title":  map[string]any{"type": "string"},
			"active": map[string]any{"type": "boolean"},
		},
		"required": []string{"tab_id", "url", "title", "active"},
	}
}

func browserStateSchema() map[string]any {
	element := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"index":       map[string]any{"type": "integer", "minimum": 0},
			"tag":         map[string]any{"type": "string"},
			"text":        map[string]any{"type": "string"},
			"aria_label":  map[string]any{"type": "string"},
			"href":        map[string]any{"type": "string"},
			"name":        map[string]any{"type": "string"},
			"placeholder": map[string]any{"type": "string"},
			"role":        map[string]any{"type": "string"},
			"type":        map[string]any{"type": "string"},
			"value":       map[string]any{"type": "string"},
		},
		"required": []string{"index", "tag", "text"},
	}
	viewport := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"width":       map[string]any{"type": "number", "minimum": 0},
			"height":      map[string]any{"type": "number", "minimum": 0},
			"scroll_x":    map[string]any{"type": "number"},
			"scroll_y":    map[string]any{"type": "number"},
			"page_width":  map[string]any{"type": "number", "minimum": 0},
			"page_height": map[string]any{"type": "number", "minimum": 0},
		},
		"required": []string{"width", "height", "scroll_x", "scroll_y", "page_width", "page_height"},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"url":                  map[string]any{"type": "string"},
			"title":                map[string]any{"type": "string"},
			"interactive_elements": map[string]any{"type": "array", "maxItems": 1000, "items": element},
			"pixels_above":         map[string]any{"type": "integer", "minimum": 0},
			"pixels_below":         map[string]any{"type": "integer", "minimum": 0},
			"viewport":             viewport,
			"tabs":                 map[string]any{"type": "array", "items": browserTabSchema()},
			"active_tab_id":        browserTabIDSchema(),
			"browser_generation":   browserGenerationSchema(),
			"state_generation":     map[string]any{"type": "integer", "minimum": 1},
		},
		"required": []string{
			"url", "title", "interactive_elements", "pixels_above", "pixels_below", "viewport",
			"tabs", "active_tab_id", "browser_generation", "state_generation",
		},
	}
}

func browserTabsSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"active_tab_id":      browserTabIDSchema(),
			"tabs":               map[string]any{"type": "array", "items": browserTabSchema()},
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"active_tab_id", "tabs", "browser_generation"},
	}
}

func browserEventStreamSchema(itemName string) map[string]any {
	properties := browserObservationSequenceProperties(itemName, browserEventSchema())
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties,
		"required": []string{
			itemName, "latest_sequence", "oldest_sequence", "next_sequence",
			"retained", "complete", "browser_generation",
		},
	}
}

func browserNetworkSchema() map[string]any {
	properties := browserObservationSequenceProperties("requests", browserNetworkRequestSchema())
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": properties,
		"required": []string{
			"requests", "latest_sequence", "oldest_sequence", "next_sequence",
			"retained", "complete", "browser_generation",
		},
	}
}

func browserRequestSchema() map[string]any {
	base := browserNetworkRequestSchema()
	properties := base["properties"].(map[string]any)
	properties["body"] = map[string]any{"type": "string"}
	properties["body_truncated"] = map[string]any{"type": "boolean"}
	properties["browser_generation"] = browserGenerationSchema()
	required := append([]string(nil), base["required"].([]string)...)
	base["required"] = append(required, "browser_generation")
	return base
}

func browserDiagnosticsSchema() map[string]any {
	summary := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"console_events":   map[string]any{"type": "integer", "minimum": 0},
			"page_errors":      map[string]any{"type": "integer", "minimum": 0},
			"network_requests": map[string]any{"type": "integer", "minimum": 0},
			"failed_requests":  map[string]any{"type": "integer", "minimum": 0},
			"websocket_events": map[string]any{"type": "integer", "minimum": 0},
			"latest_sequence":  map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{
			"console_events", "page_errors", "network_requests",
			"failed_requests", "websocket_events", "latest_sequence",
		},
	}
	page := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"url": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
		},
		"required": []string{"url", "title"},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"summary":                summary,
			"recent_console":         map[string]any{"type": "array", "items": browserEventSchema()},
			"recent_page_errors":     map[string]any{"type": "array", "items": browserEventSchema()},
			"recent_failed_requests": map[string]any{"type": "array", "items": browserNetworkRequestSchema()},
			"recent_websockets":      map[string]any{"type": "array", "items": browserEventSchema()},
			"page":                   page,
			"latest_sequence":        map[string]any{"type": "integer", "minimum": 0},
			"oldest_sequence":        map[string]any{"type": "integer", "minimum": 0},
			"next_sequence":          map[string]any{"anyOf": []any{map[string]any{"type": "integer", "minimum": 0}, map[string]any{"type": "null"}}},
			"complete":               map[string]any{"type": "boolean"},
			"browser_generation":     browserGenerationSchema(),
		},
		"required": []string{
			"summary", "recent_console", "recent_page_errors", "recent_failed_requests", "recent_websockets",
			"page", "latest_sequence", "oldest_sequence", "next_sequence", "complete", "browser_generation",
		},
	}
}

func browserDialogSchema() map[string]any {
	generation := map[string]any{"type": "integer", "minimum": 0}
	notPending := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"pending":            map[string]any{"const": false},
			"dialog_generation":  generation,
			"browser_generation": browserGenerationSchema(),
		},
		"required": []string{"pending", "dialog_generation", "browser_generation"},
	}
	pendingBase := func(types []string, accepts bool, prompt bool) map[string]any {
		properties := map[string]any{
			"pending":            map[string]any{"const": true},
			"type":               map[string]any{"type": "string", "enum": types},
			"message":            map[string]any{"type": "string", "maxLength": 4096},
			"accepts_prompt":     map[string]any{"const": accepts},
			"dialog_generation":  map[string]any{"type": "integer", "minimum": 1},
			"browser_generation": browserGenerationSchema(),
		}
		required := []string{"pending", "type", "message", "accepts_prompt", "dialog_generation", "browser_generation"}
		if prompt {
			properties["default_prompt"] = map[string]any{"type": "string", "maxLength": 4096}
			required = append(required, "default_prompt")
		}
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": properties, "required": required,
		}
	}
	return map[string]any{
		"type": "object",
		"oneOf": []any{
			notPending,
			pendingBase([]string{"prompt"}, true, true),
			pendingBase([]string{"alert", "confirm", "beforeunload"}, false, false),
		},
	}
}

func overrideBrowserObserve(tool *mcp.Tool) error {
	input, err := (ActionInputContract{
		Title:             "browser_observeArguments",
		ActionDescription: "Browser observation to perform.",
		DefaultAction:     "state",
		Fields: []ActionField{
			{Name: "since_sequence", Schema: map[string]any{
				"type": "integer", "minimum": 0, "maximum": 2147483647, "default": 0,
				"description": "Return retained observations newer than this sequence number.",
			}},
			{Name: "limit", Schema: map[string]any{
				"type": "integer", "minimum": 1, "maximum": 500, "default": 100,
				"description": "Maximum retained entries to return for event-like observations.",
			}},
			{Name: "level", Schema: map[string]any{
				"type": "string", "maxLength": 50,
				"description": "Optional console level filter for action=console.",
			}},
			{Name: "status_min", Schema: map[string]any{
				"type": "integer", "minimum": 100, "maximum": 599,
				"description": "Minimum HTTP response status for action=network.",
			}},
			{Name: "failed_only", Schema: map[string]any{
				"type": "boolean", "default": false,
				"description": "Return only failed/error HTTP requests for action=network.",
			}},
			{Name: "resource_type", Schema: map[string]any{
				"type": "string", "maxLength": 50,
				"description": "Case-insensitive CDP resource type filter for action=network.",
			}},
			{Name: "request_id", Schema: map[string]any{
				"type": "string", "minLength": 1, "maxLength": 300,
				"description": "Retained browser network request identifier returned by action=network.",
			}},
			{Name: "include_body", Schema: map[string]any{
				"type": "boolean", "default": false,
				"description": "Include a bounded text-like response body for action=request.",
			}},
			{Name: "max_body_chars", Schema: map[string]any{
				"type": "integer", "minimum": 1, "maximum": 262144, "default": 65536,
				"description": "Maximum decoded response-body characters returned by action=request.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "state"},
			{Name: "tabs"},
			{Name: "console", Optional: []string{"since_sequence", "limit", "level"}},
			{Name: "network", Optional: []string{"since_sequence", "limit", "status_min", "failed_only", "resource_type"}},
			{Name: "request", Required: []string{"request_id"}, Optional: []string{"include_body", "max_body_chars"}},
			{Name: "websockets", Optional: []string{"since_sequence", "limit"}},
			{Name: "errors", Optional: []string{"since_sequence", "limit"}},
			{Name: "diagnostics", Optional: []string{"since_sequence", "limit"}, Overrides: map[string]map[string]any{
				"limit": {"maximum": 200, "default": 50},
			}},
			{Name: "dialog"},
		},
	}).Schema()
	if err != nil {
		return err
	}
	tool.Description = "Observe current browser/page state, pending JavaScript dialogs, or retained browser diagnostics with action-specific filters. state returns browser_generation and state_generation for safe element references; dialog returns a monotonic dialog_generation; event-like results report sequence retention and complete/next_sequence truthfully."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{
		"type": "object",
		"oneOf": []any{
			browserStateSchema(),
			browserTabsSchema(),
			browserEventStreamSchema("events"),
			browserEventStreamSchema("errors"),
			browserNetworkSchema(),
			browserRequestSchema(),
			browserDiagnosticsSchema(),
			browserDialogSchema(),
		},
	}
	return nil
}
