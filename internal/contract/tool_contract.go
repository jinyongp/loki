package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ActionField struct {
	Name   string
	Schema map[string]any
}

type ActionVariant struct {
	Name      string
	Required  []string
	Optional  []string
	Overrides map[string]map[string]any
}

type ActionInputContract struct {
	Title             string
	ActionDescription string
	DefaultAction     string
	Fields            []ActionField
	Variants          []ActionVariant
}

func cloneSchema(value map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func (c ActionInputContract) Schema() (map[string]any, error) {
	if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.ActionDescription) == "" {
		return nil, errors.New("action contract requires title and action description")
	}
	if len(c.Variants) == 0 {
		return nil, errors.New("action contract requires at least one variant")
	}

	fieldSchemas := make(map[string]map[string]any, len(c.Fields))
	for _, field := range c.Fields {
		if field.Name == "" || field.Name == "action" {
			return nil, errors.New("action contract has invalid field name")
		}
		if fieldSchemas[field.Name] != nil {
			return nil, fmt.Errorf("action contract field %q is duplicated", field.Name)
		}
		description, _ := field.Schema["description"].(string)
		if strings.TrimSpace(description) == "" {
			return nil, fmt.Errorf("action contract field %q requires a description", field.Name)
		}
		schema, err := cloneSchema(field.Schema)
		if err != nil {
			return nil, err
		}
		fieldSchemas[field.Name] = schema
	}

	actionNames := make([]string, 0, len(c.Variants))
	seenActions := map[string]bool{}
	defaultSeen := c.DefaultAction == ""
	branches := make([]any, 0, len(c.Variants))
	for _, variant := range c.Variants {
		if variant.Name == "" || seenActions[variant.Name] {
			return nil, errors.New("action contract has an empty or duplicate variant")
		}
		seenActions[variant.Name] = true
		actionNames = append(actionNames, variant.Name)
		if variant.Name == c.DefaultAction {
			defaultSeen = true
		}

		allowed := make(map[string]bool, len(variant.Required)+len(variant.Optional))
		required := []string{}
		if variant.Name != c.DefaultAction {
			required = append(required, "action")
		}
		properties := map[string]any{
			"action": map[string]any{
				"type":        "string",
				"const":       variant.Name,
				"description": c.ActionDescription,
			},
		}
		for _, name := range append(append([]string{}, variant.Required...), variant.Optional...) {
			if allowed[name] {
				return nil, fmt.Errorf("action %q repeats field %q", variant.Name, name)
			}
			schema := fieldSchemas[name]
			if schema == nil {
				return nil, fmt.Errorf("action %q references unknown field %q", variant.Name, name)
			}
			allowed[name] = true
			cloned, err := cloneSchema(schema)
			if err != nil {
				return nil, err
			}
			if override := variant.Overrides[name]; override != nil {
				overrideClone, err := cloneSchema(override)
				if err != nil {
					return nil, err
				}
				for key, value := range overrideClone {
					cloned[key] = value
				}
			}
			properties[name] = cloned
		}
		for name := range variant.Overrides {
			if !allowed[name] {
				return nil, fmt.Errorf("action %q overrides unavailable field %q", variant.Name, name)
			}
		}
		for _, name := range variant.Required {
			required = append(required, name)
		}
		branches = append(branches, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           properties,
			"required":             required,
		})
	}
	if !defaultSeen {
		return nil, fmt.Errorf("default action %q is not a declared variant", c.DefaultAction)
	}
	sort.Strings(actionNames)

	actionSchema := map[string]any{
		"type":        "string",
		"enum":        actionNames,
		"description": c.ActionDescription,
	}
	rootRequired := []string{"action"}
	if c.DefaultAction != "" {
		actionSchema["default"] = c.DefaultAction
		rootRequired = nil
	}
	rootProperties := map[string]any{
		"action": actionSchema,
	}
	fieldNames := make([]string, 0, len(fieldSchemas))
	for name := range fieldSchemas {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)
	for _, name := range fieldNames {
		cloned, err := cloneSchema(fieldSchemas[name])
		if err != nil {
			return nil, err
		}
		rootProperties[name] = cloned
	}

	result := map[string]any{
		"type":                 "object",
		"title":                c.Title,
		"additionalProperties": false,
		"properties":           rootProperties,
		"oneOf":                branches,
	}
	if len(rootRequired) > 0 {
		result["required"] = rootRequired
	}
	return result, nil
}

type toolOverride func(*mcp.Tool) error

type toolFactory func() (*mcp.Tool, error)

func boolPointer(value bool) *bool { return &value }

func retiredSnapshotTools() map[string]bool {
	return map[string]bool{"github_issue_fields": true}
}

func generatedStandaloneTools() map[string]toolFactory {
	return map[string]toolFactory{
		"github_issue_fields_read":  githubIssueFieldsReadTool,
		"github_issue_fields_write": githubIssueFieldsWriteTool,
	}
}

func issueFieldOptionSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id":    map[string]any{"type": "integer"},
			"name":  map[string]any{"type": "string"},
			"color": map[string]any{"type": "string"},
		},
		"required": []string{"id", "name", "color"},
	}
}

func issueFieldSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id":          map[string]any{"type": "integer"},
			"node_id":     map[string]any{"type": "string"},
			"name":        map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"data_type":   map[string]any{"type": "string"},
			"options": map[string]any{
				"type": "array", "items": issueFieldOptionSchema(),
			},
		},
		"required": []string{"id", "node_id", "name", "description", "data_type"},
	}
}

func issueFieldValueSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"issue_field_id":   map[string]any{"type": "integer"},
			"issue_field_name": map[string]any{"type": "string"},
			"node_id":          map[string]any{"type": "string"},
			"data_type":        map[string]any{"type": "string"},
			"value":            map[string]any{},
			"single_select_option": map[string]any{
				"anyOf": []any{issueFieldOptionSchema(), map[string]any{"type": "null"}},
			},
			"multi_select_options": map[string]any{
				"type": "array", "items": issueFieldOptionSchema(),
			},
		},
		"required": []string{"issue_field_id", "issue_field_name", "node_id", "data_type", "value"},
	}
}

func issueFieldValueInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"field_id":  map[string]any{"type": "integer", "minimum": 1},
			"data_type": map[string]any{"type": "string", "enum": []string{"text", "single_select", "number", "date", "multi_select"}},
			"text":      map[string]any{"type": "string"},
			"number":    map[string]any{"type": "number"},
			"options": map[string]any{
				"type": "array", "maxItems": 25, "items": map[string]any{"type": "string"},
			},
		},
		"required": []string{"field_id", "data_type"},
	}
}

func githubIssueFieldsReadTool() (*mcp.Tool, error) {
	input, err := (ActionInputContract{
		Title:             "github_issue_fields_readArguments",
		ActionDescription: "GitHub Issue Fields read operation to perform.",
		Fields: []ActionField{
			{Name: "target", Schema: map[string]any{
				"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$",
				"description": "Configured owner/repository target.",
			}},
			{Name: "issue", Schema: map[string]any{
				"type": "integer", "minimum": 1,
				"description": "Positive issue number whose Issue Field values should be listed.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "list_fields", Required: []string{"target"}},
			{Name: "list_values", Required: []string{"target", "issue"}},
		},
	}).Schema()
	if err != nil {
		return nil, err
	}
	return &mcp.Tool{
		Name:        "github_issue_fields_read",
		Description: "Read configured GitHub organization Issue Fields or the typed Issue Field values attached to one issue.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: boolPointer(false), IdempotentHint: true,
			OpenWorldHint: boolPointer(true), ReadOnlyHint: true,
		},
		InputSchema: input,
		OutputSchema: map[string]any{
			"type": "object",
			"oneOf": []any{
				map[string]any{
					"additionalProperties": false,
					"properties":           map[string]any{"fields": map[string]any{"type": "array", "items": issueFieldSchema()}},
					"required":             []string{"fields"},
				},
				map[string]any{
					"additionalProperties": false,
					"properties":           map[string]any{"values": map[string]any{"type": "array", "items": issueFieldValueSchema()}},
					"required":             []string{"values"},
				},
			},
		},
	}, nil
}

func githubIssueFieldsWriteTool() (*mcp.Tool, error) {
	input, err := (ActionInputContract{
		Title:             "github_issue_fields_writeArguments",
		ActionDescription: "GitHub Issue Fields mutation to perform.",
		Fields: []ActionField{
			{Name: "target", Schema: map[string]any{
				"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$",
				"description": "Configured owner/repository target.",
			}},
			{Name: "issue", Schema: map[string]any{
				"type": "integer", "minimum": 1,
				"description": "Positive issue number whose Issue Field values should be changed.",
			}},
			{Name: "field_id", Schema: map[string]any{
				"type": "integer", "minimum": 1,
				"description": "Positive Issue Field identifier to clear.",
			}},
			{Name: "values", Schema: map[string]any{
				"type": "array", "minItems": 1, "maxItems": 25, "items": issueFieldValueInputSchema(),
				"description": "Typed Issue Field values to add or replace. Each item identifies its field and data type.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "add_values", Required: []string{"target", "issue", "values"}},
			{Name: "set_values", Required: []string{"target", "issue", "values"}},
			{Name: "clear_value", Required: []string{"target", "issue", "field_id"}},
		},
	}).Schema()
	if err != nil {
		return nil, err
	}
	tool := &mcp.Tool{
		Name:        "github_issue_fields_write",
		Description: "Mutate configured GitHub Issue Field values by adding typed values, replacing the complete typed value set, or clearing one field value.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: boolPointer(true), IdempotentHint: false,
			OpenWorldHint: boolPointer(true), ReadOnlyHint: false,
		},
		InputSchema: input,
		OutputSchema: map[string]any{
			"type": "object",
			"oneOf": []any{
				map[string]any{
					"additionalProperties": false,
					"properties":           map[string]any{"values": map[string]any{"type": "array", "items": issueFieldValueSchema()}},
					"required":             []string{"values"},
				},
				map[string]any{
					"additionalProperties": false,
					"properties":           map[string]any{"cleared": map[string]any{"type": "boolean"}},
					"required":             []string{"cleared"},
				},
			},
		},
	}
	if err := ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"add_values": {
			Replay: ReplayUnsafe, FailureAtomicity: FailureUpstream, CrashRecovery: CrashRecoveryUpstream,
			AffectedResourceLimit: 25, RecoveryReference: "system_inspect action=operation",
		},
		"set_values": {
			Replay: ReplayUnsafe, FailureAtomicity: FailureUpstream, CrashRecovery: CrashRecoveryUpstream,
			AffectedResourceLimit: 25, RecoveryReference: "system_inspect action=operation",
		},
		"clear_value": {
			Replay: ReplayUnsafe, FailureAtomicity: FailureUpstream, CrashRecovery: CrashRecoveryUpstream,
			AffectedResourceLimit: 1, RecoveryReference: "system_inspect action=operation",
		},
	}); err != nil {
		return nil, err
	}
	return tool, nil
}

func generatedToolOverrides() map[string]toolOverride {
	return map[string]toolOverride{
		"system_inspect":             overrideSystemInspect,
		"preview_publish":            overridePreviewPublish,
		"shared_resources":           overrideSharedResources,
		"revoke_share":               overrideRevokeShare,
		"browser_session":            overrideBrowserSession,
		"browser_observe":            overrideBrowserObserve,
		"browser_interact":           overrideBrowserInteract,
		"browser_screenshot":         overrideBrowserScreenshot,
		"browser_save_screenshot":    overrideBrowserSaveScreenshot,
		"browser_share_screenshot":   overrideBrowserShareScreenshot,
		"read_image":                 overrideReadImage,
		"share_image":                overrideShareImage,
		"artifact_publish":           overrideArtifactPublish,
		"write_image":                overrideWriteImage,
		"secret_inspect":             overrideSecretInspect,
		"secret_write":               overrideSecretWrite,
		"secret_delete":              overrideSecretDelete,
		"developer_view":             overrideDeveloperView,
		"agent_guidance":             overrideAgentGuidance,
		"project_coordination":       overrideProjectCoordination,
		"project_coordination_write": overrideProjectCoordinationWrite,
		"project_context":            overrideProjectContext,
		"project_context_write":      overrideProjectContextWrite,
		"github":                     overrideGitHub,
		"workspace_read":             overrideWorkspaceRead,
		"workspace_edit":             overrideWorkspaceEdit,
		"restore_workspace_file":     overrideRestoreWorkspaceFile,
		"remove_tracked_file":        overrideRemoveTrackedFile,
		"git_inspect":                overrideGitInspect,
		"git_stage":                  overrideGitStage,
	}
}

func overrideSystemInspect(tool *mcp.Tool) error {
	schema, err := (ActionInputContract{
		Title:             "system_inspectArguments",
		ActionDescription: "Loki system inspection operation to perform.",
		DefaultAction:     "server",
		Fields: []ActionField{
			{Name: "port", Schema: map[string]any{
				"type": "integer", "minimum": 1, "maximum": 65535,
				"description": "TCP port to inspect for workspace-owned listener information.",
			}},
			{Name: "limit", Schema: map[string]any{
				"type": "integer", "minimum": 1, "maximum": 50, "default": 20,
				"description": "Maximum number of recent retained tool operations to return.",
			}},
			{Name: "correlation_id", Schema: map[string]any{
				"type": "string", "pattern": "^[0-9a-f]{16}-[0-9a-f]{16}$",
				"description": "Correlation identifier returned in Loki tool result metadata or a typed tool error.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "server"},
			{Name: "diagnostics"},
			{Name: "workspace"},
			{Name: "activity", Optional: []string{"limit"}},
			{Name: "operation", Required: []string{"correlation_id"}},
			{Name: "port", Required: []string{"port"}},
		},
	}).Schema()
	if err != nil {
		return err
	}
	tool.Description = "Inspect Loki server/workspace health, recent tool activity, one retained operation by correlation ID, or one workspace TCP port."
	tool.InputSchema = schema
	tool.OutputSchema = systemInspectOutputSchema()
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = true
		tool.Annotations.DestructiveHint = boolPointer(false)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(false)
	}
	return nil
}

func overrideWorkspaceRead(tool *mcp.Tool) error {
	input, err := (ActionInputContract{
		Title:             "workspace_readArguments",
		ActionDescription: "Workspace read/history operation to perform.",
		Fields: []ActionField{
			{Name: "path", Schema: map[string]any{
				"type": "string", "minLength": 1, "default": ".",
				"description": "Workspace-relative path. list/search default to the workspace root; file/history actions require an explicit target path.",
			}},
			{Name: "max_depth", Schema: map[string]any{
				"type": "integer", "minimum": 1, "maximum": 10, "default": 3,
				"description": "Maximum recursive directory depth for action=list.",
			}},
			{Name: "offset", Schema: map[string]any{
				"type": "integer", "minimum": 0, "default": 0,
				"description": "Zero-based entry or line offset for action=list or action=file.",
			}},
			{Name: "limit", Schema: map[string]any{
				"type": "integer", "minimum": 1, "maximum": 100000, "default": 200,
				"description": "Requested entry, line, or revision count. Loki clamps it to the action-specific configured bound.",
			}},
			{Name: "query", Schema: map[string]any{
				"type": "string", "minLength": 1, "maxLength": 500,
				"description": "Literal or regular-expression search text for action=search.",
			}},
			{Name: "max_results", Schema: map[string]any{
				"type": "integer", "minimum": 1, "maximum": 10000, "default": 100,
				"description": "Maximum search matches requested for action=search; the configured server bound may be lower.",
			}},
			{Name: "regex", Schema: map[string]any{
				"type": "boolean", "default": false,
				"description": "Interpret query as a regular expression for action=search; false performs literal search.",
			}},
			{Name: "revision", Schema: map[string]any{
				"type": "string", "pattern": "^[0-9a-f]{64}$",
				"description": "Saved file revision identifier for action=revision_diff.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "list", Optional: []string{"path", "max_depth", "offset", "limit"}},
			{Name: "file", Required: []string{"path"}, Optional: []string{"offset", "limit"}},
			{Name: "search", Required: []string{"query"}, Optional: []string{"path", "max_results", "regex"}},
			{Name: "revisions", Required: []string{"path"}, Optional: []string{"limit"}},
			{Name: "revision_diff", Required: []string{"path", "revision"}},
		},
	}).Schema()
	if err != nil {
		return err
	}

	entry := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
			"type": map[string]any{"type": "string", "enum": []string{"file", "directory"}},
			"size": map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{"path", "type"},
	}
	match := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":   map[string]any{"type": "string"},
			"line":   map[string]any{"type": "integer", "minimum": 1},
			"column": map[string]any{"type": "integer", "minimum": 1},
			"text":   map[string]any{"type": "string"},
		},
		"required": []string{"path", "line", "column", "text"},
	}
	revision := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"revision":   map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"path":       map[string]any{"type": "string"},
			"operation":  map[string]any{"type": "string"},
			"created_at": map[string]any{"type": "string"},
			"bytes":      map[string]any{"type": "integer", "minimum": 0},
			"sha256":     map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"mode":       map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{"revision", "path", "operation", "created_at", "bytes", "sha256", "mode"},
	}
	listResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"entries":     map[string]any{"type": "array", "items": entry},
			"offset":      map[string]any{"type": "integer", "minimum": 0},
			"next_offset": map[string]any{"type": "integer", "minimum": 0},
			"has_more":    map[string]any{"type": "boolean"},
		},
		"required": []string{"entries", "offset", "next_offset", "has_more"},
	}
	fileResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":        map[string]any{"type": "string"},
			"offset":      map[string]any{"type": "integer", "minimum": 0},
			"next_offset": map[string]any{"type": "integer", "minimum": 0},
			"start_line":  map[string]any{"type": "integer", "minimum": 1},
			"end_line":    map[string]any{"type": "integer", "minimum": 0},
			"content":     map[string]any{"type": "string"},
			"eof":         map[string]any{"type": "boolean"},
			"size":        map[string]any{"type": "integer", "minimum": 0},
			"sha256":      map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
		},
		"required": []string{"path", "offset", "next_offset", "start_line", "end_line", "content", "eof", "size", "sha256"},
	}
	searchResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"matches":   map[string]any{"type": "array", "items": match},
			"truncated": map[string]any{"type": "boolean"},
		},
		"required": []string{"matches", "truncated"},
	}
	revisionsResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":      map[string]any{"type": "string"},
			"revisions": map[string]any{"type": "array", "items": revision},
		},
		"required": []string{"path", "revisions"},
	}
	binaryDiff := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":     map[string]any{"type": "string"},
			"revision": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"binary":   map[string]any{"const": true},
			"diff":     map[string]any{"type": "null"},
		},
		"required": []string{"path", "revision", "binary", "diff"},
	}
	textDiff := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":            map[string]any{"type": "string"},
			"revision":        map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"binary":          map[string]any{"const": false},
			"diff":            map[string]any{"type": "string"},
			"truncated":       map[string]any{"type": "boolean"},
			"previous_sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"current_sha256":  map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
		},
		"required": []string{"path", "revision", "binary", "diff", "truncated", "previous_sha256", "current_sha256"},
	}

	tool.Description = "Read workspace text and recoverable history with action-specific inputs. list exposes has_more/next_offset, file exposes eof/next_offset, and search exposes truncated so incomplete results cannot be mistaken for exhaustive coverage."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{
		"type":  "object",
		"oneOf": []any{listResult, fileResult, searchResult, revisionsResult, binaryDiff, textDiff},
	}
	return nil
}

func overrideRestoreWorkspaceFile(tool *mcp.Tool) error {
	tool.Description = "Restore one saved pre-mutation file revision after verifying the currently observed target digest or confirmed absence."
	tool.InputSchema = map[string]any{
		"type":                 "object",
		"title":                "restore_workspace_fileArguments",
		"additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{
				"type": "string", "minLength": 1,
				"description": "Workspace-relative file path that the saved revision belongs to.",
			},
			"revision": map[string]any{
				"type": "string", "pattern": "^[0-9a-f]{64}$",
				"description": "Saved pre-mutation revision identifier returned by a prior workspace mutation.",
			},
			"expected_sha256": map[string]any{
				"anyOf": []any{
					map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
					map[string]any{"const": "missing"},
				},
				"description": "Current target digest observed immediately before restore, or the literal missing after confirming the target is absent.",
			},
		},
		"required": []string{"path", "revision", "expected_sha256"},
	}
	tool.OutputSchema = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path":              map[string]any{"type": "string"},
			"restored_revision": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"undo_revision": map[string]any{"anyOf": []any{
				map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
				map[string]any{"type": "null"},
			}},
			"sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"bytes":  map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{"path", "restored_revision", "undo_revision", "sha256", "bytes"},
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"restore": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_sha256"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "workspace_read action=revisions",
		},
	})
}

func overrideRemoveTrackedFile(tool *mcp.Tool) error {
	tool.Description = "Remove one Git-tracked file only if its current SHA-256 still matches the caller's observed digest; returns a revision that can restore the removed content."
	tool.InputSchema = map[string]any{
		"type":                 "object",
		"title":                "remove_tracked_fileArguments",
		"additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{
				"type": "string", "minLength": 1,
				"description": "Workspace-relative Git-tracked regular file path to remove.",
			},
			"expected_sha256": map[string]any{
				"type": "string", "pattern": "^[0-9a-f]{64}$",
				"description": "SHA-256 digest observed when the file was read; removal fails if the content changed.",
			},
		},
		"required": []string{"path", "expected_sha256"},
	}
	tool.OutputSchema = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path":              map[string]any{"type": "string"},
			"removed":           map[string]any{"type": "boolean"},
			"previous_revision": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"previous_sha256":   map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
		},
		"required": []string{"path", "removed", "previous_revision", "previous_sha256"},
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"remove": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_sha256"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "restore_workspace_file with previous_revision",
		},
	})
}

func workspaceBatchOperationSchema() map[string]any {
	path := func(description string) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "description": description}
	}
	digest := func(description string) map[string]any {
		return map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": description}
	}
	return map[string]any{
		"oneOf": []any{
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"action":          map[string]any{"const": "create", "description": "Create one absent text file."},
					"path":            path("Absent workspace-relative file path to create."),
					"content":         map[string]any{"type": "string", "description": "Complete UTF-8 file content."},
					"expected_sha256": map[string]any{"const": "missing", "description": "Explicit absence precondition; must be the literal missing."},
				},
				"required": []string{"action", "path", "content", "expected_sha256"},
			},
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"action":                map[string]any{"const": "replace", "description": "Replace exact text in one existing text file."},
					"path":                  path("Existing workspace-relative text file path."),
					"old":                   map[string]any{"type": "string", "minLength": 1, "description": "Exact non-empty text to replace."},
					"new":                   map[string]any{"type": "string", "description": "Replacement text; may be empty."},
					"expected_sha256":       digest("SHA-256 observed when the file was read."),
					"expected_replacements": map[string]any{"type": "integer", "minimum": 1, "description": "Exact number of old-text matches required."},
				},
				"required": []string{"action", "path", "old", "new", "expected_sha256", "expected_replacements"},
			},
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"action":               map[string]any{"const": "move", "description": "Move one existing file to one absent destination."},
					"source":               path("Existing workspace-relative source file path."),
					"destination":          path("Absent workspace-relative destination path."),
					"expected_sha256":      digest("SHA-256 observed for the source file."),
					"expected_destination": map[string]any{"const": "missing", "description": "Explicit destination absence precondition; must be the literal missing."},
				},
				"required": []string{"action", "source", "destination", "expected_sha256", "expected_destination"},
			},
		},
	}
}

func overrideWorkspaceEdit(tool *mcp.Tool) error {
	schema, err := (ActionInputContract{
		Title:             "workspace_editArguments",
		ActionDescription: "Workspace edit operation to perform.",
		Fields: []ActionField{
			{
				Name: "path",
				Schema: map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Workspace-relative file path used by create or replace.",
				},
			},
			{
				Name: "content",
				Schema: map[string]any{
					"type":        "string",
					"description": "Complete UTF-8 content for a newly created file.",
				},
			},
			{
				Name: "old",
				Schema: map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Exact non-empty text expected in the current file.",
				},
			},
			{
				Name: "new",
				Schema: map[string]any{
					"type":        "string",
					"description": "Replacement text; may be empty.",
				},
			},
			{
				Name: "expected_sha256",
				Schema: map[string]any{
					"type":        "string",
					"pattern":     "^[0-9a-f]{64}$",
					"description": "SHA-256 digest observed when the file was read; replace fails if it changed.",
				},
			},
			{
				Name: "expected_replacements",
				Schema: map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Exact number of old-text matches that must exist before replacement.",
				},
			},
			{
				Name: "patch",
				Schema: map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "One unified diff. A single call may create or modify multiple files up to Loki's configured patch-file limit after whole-patch preflight.",
				},
			},
			{
				Name: "source",
				Schema: map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Existing workspace-relative source file path for move.",
				},
			},
			{
				Name: "destination",
				Schema: map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Absent workspace-relative destination path for move.",
				},
			},
			{
				Name:   "request_id",
				Schema: RequestIDSchema("Caller-owned UUID used to replay one structured workspace batch without applying it twice."),
			},
			{
				Name: "operations",
				Schema: map[string]any{
					"type":        "array",
					"minItems":    1,
					"maxItems":    50,
					"description": "Bounded create/replace/move operations. Every path may appear at most once across the complete batch.",
					"items":       workspaceBatchOperationSchema(),
				},
			},
		},
		Variants: []ActionVariant{
			{Name: "create", Required: []string{"path", "content"}},
			{Name: "replace", Required: []string{"path", "old", "new", "expected_sha256", "expected_replacements"}},
			{Name: "patch", Required: []string{"patch"}},
			{Name: "move", Required: []string{"source", "destination"}},
			{Name: "batch", Required: []string{"request_id", "operations"}},
		},
	}).Schema()
	if err != nil {
		return err
	}
	fileDigest := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	revision := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	createResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":  map[string]any{"type": "string"},
			"bytes": map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{"path", "bytes"},
	}
	replaceResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":              map[string]any{"type": "string"},
			"replacements":      map[string]any{"type": "integer", "minimum": 1},
			"previous_revision": revision,
			"sha256":            fileDigest,
		},
		"required": []string{"path", "replacements", "previous_revision", "sha256"},
	}
	patchFile := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"added":   map[string]any{"type": "integer", "minimum": 0},
			"deleted": map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{"path", "added", "deleted"},
	}
	patchResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"files":        map[string]any{"type": "array", "items": patchFile},
			"patch_sha256": fileDigest,
			"warnings":     map[string]any{"type": "string"},
			"previous_revisions": map[string]any{
				"type": "object", "additionalProperties": revision,
			},
		},
		"required": []string{"files", "patch_sha256", "warnings", "previous_revisions"},
	}
	moveResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"source":            map[string]any{"type": "string"},
			"destination":       map[string]any{"type": "string"},
			"previous_revision": revision,
		},
		"required": []string{"source", "destination", "previous_revision"},
	}
	batchFile := map[string]any{
		"oneOf": []any{
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"action": map[string]any{"const": "create"},
					"path":   map[string]any{"type": "string"},
					"sha256": fileDigest,
				},
				"required": []string{"action", "path", "sha256"},
			},
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"action":            map[string]any{"const": "replace"},
					"path":              map[string]any{"type": "string"},
					"sha256":            fileDigest,
					"previous_revision": revision,
				},
				"required": []string{"action", "path", "sha256", "previous_revision"},
			},
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"action":            map[string]any{"const": "move"},
					"source":            map[string]any{"type": "string"},
					"destination":       map[string]any{"type": "string"},
					"sha256":            fileDigest,
					"previous_revision": revision,
				},
				"required": []string{"action", "source", "destination", "sha256", "previous_revision"},
			},
		},
	}
	batchResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"request_id":   map[string]any{"type": "string", "pattern": RequestIDPattern},
			"operation_id": fileDigest,
			"state":        map[string]any{"const": "applied"},
			"files":        map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": batchFile},
		},
		"required": []string{"request_id", "operation_id", "state", "files"},
	}

	tool.Description = "Edit workspace text with action-specific preconditions. create, replace, and move operate on one path; patch applies one preflighted unified diff across multiple files; batch applies up to 50 guarded create/replace/move operations with request-ID replay, rollback, and restart reconciliation."
	tool.InputSchema = schema
	tool.OutputSchema = map[string]any{
		"type":  "object",
		"oneOf": []any{createResult, replaceResult, patchResult, moveResult, batchResult},
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"create": {
			Replay: ReplayUnsafe, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "workspace_read action=file",
		},
		"replace": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_sha256"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "restore_workspace_file with previous_revision",
		},
		"patch": {
			Replay: ReplayUnsafe, FailureAtomicity: FailurePreflight, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1000, RecoveryReference: "workspace_read action=revisions",
		},
		"move": {
			Replay: ReplayUnsafe, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "restore_workspace_file with previous_revision",
		},
		"batch": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			FailureAtomicity: FailureRollback, CrashRecovery: CrashRecoveryJournaled,
			AffectedResourceLimit: 50, RecoveryReference: "replay the same workspace_edit batch request_id",
		},
	})
}

func gitProcessResultSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"exit_code": map[string]any{"type": "integer"},
			"output":    map[string]any{"type": "string"},
			"truncated": map[string]any{"type": "boolean"},
			"timed_out": map[string]any{"type": "boolean"},
		},
		"required": []string{"exit_code", "output", "truncated"},
	}
}

func overrideGitInspect(tool *mcp.Tool) error {
	input, err := (ActionInputContract{
		Title:             "git_inspectArguments",
		ActionDescription: "Git repository inspection operation to perform.",
		DefaultAction:     "status",
		Fields: []ActionField{
			{Name: "cwd", Schema: map[string]any{
				"type": "string", "minLength": 1, "default": ".",
				"description": "Workspace-relative directory inside the target Git repository; /workspace absolute cwd is also accepted by the repository resolver.",
			}},
			{Name: "path", Schema: map[string]any{
				"type": "string", "minLength": 1,
				"description": "Optional repository-relative path filter for action=diff, including known deleted or renamed paths.",
			}},
			{Name: "staged", Schema: map[string]any{
				"type": "boolean", "default": false,
				"description": "Inspect the staged index diff instead of the worktree diff; valid only for action=diff.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "status", Optional: []string{"cwd"}},
			{Name: "diff", Optional: []string{"cwd", "path", "staged"}},
			{Name: "index", Optional: []string{"cwd"}},
			{Name: "commit_context", Optional: []string{"cwd"}},
		},
	}).Schema()
	if err != nil {
		return err
	}
	origin := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"source": map[string]any{"type": "string"},
			"value":  map[string]any{"type": "string"},
		},
		"required": []string{"source", "value"},
	}
	unconfigured := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"configured": map[string]any{"const": false},
			"origins":    map[string]any{"type": "array", "items": origin},
			"template":   map[string]any{"type": "null"},
		},
		"required": []string{"configured", "origins", "template"},
	}
	configured := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"configured": map[string]any{"const": true},
			"origins":    map[string]any{"type": "array", "items": origin},
			"template": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
					"sha256":  map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
				},
				"required": []string{"path", "content", "sha256"},
			},
		},
		"required": []string{"configured", "origins", "template"},
	}
	tool.Description = "Inspect Git status, bounded diff output, the complete index digest used for mutation CAS, or trusted commit-template context with action-specific inputs."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{
		"type": "object",
		"oneOf": []any{
			gitProcessResultSchema(),
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"cwd":          map[string]any{"type": "string"},
					"index_sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
				},
				"required": []string{"cwd", "index_sha256"},
			},
			unconfigured,
			configured,
		},
	}
	return nil
}

func overrideGitStage(tool *mcp.Tool) error {
	schema, err := (ActionInputContract{
		Title:             "git_stageArguments",
		ActionDescription: "Git index mutation to perform.",
		Fields: []ActionField{
			{
				Name: "cwd",
				Schema: map[string]any{
					"type":        "string",
					"minLength":   1,
					"default":     ".",
					"description": "Workspace-relative directory inside the target Git repository.",
				},
			},
			{
				Name: "paths",
				Schema: map[string]any{
					"type":        "array",
					"minItems":    1,
					"uniqueItems": true,
					"description": "One or more workspace-relative paths to stage or unstage in a single index mutation, bounded by the configured patch-file limit.",
					"items": map[string]any{
						"type":      "string",
						"minLength": 1,
					},
				},
			},
			{
				Name: "patch",
				Schema: map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Unified text diff for partial staging. One patch may span multiple files up to the configured patch-file limit.",
				},
			},
			{
				Name: "reverse",
				Schema: map[string]any{
					"type":        "boolean",
					"default":     false,
					"description": "Apply the staging patch in reverse; valid only for action=patch.",
				},
			},
			{
				Name: "expected_index_sha256",
				Schema: map[string]any{
					"type":        "string",
					"pattern":     "^[0-9a-f]{64}$",
					"description": "SHA-256 digest returned by git_inspect action=index. The mutation fails if the Git index changed since inspection.",
				},
			},
		},
		Variants: []ActionVariant{
			{Name: "paths", Required: []string{"paths", "expected_index_sha256"}, Optional: []string{"cwd"}},
			{Name: "unstage", Required: []string{"paths", "expected_index_sha256"}, Optional: []string{"cwd"}},
			{Name: "patch", Required: []string{"patch", "expected_index_sha256"}, Optional: []string{"cwd", "reverse"}},
		},
	}).Schema()
	if err != nil {
		return err
	}
	digest := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	pathResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"operation": map[string]any{"type": "string", "enum": []string{"stage", "unstage"}},
			"paths": map[string]any{
				"type": "array", "minItems": 1, "items": map[string]any{"type": "string"},
			},
			"previous_index_sha256": digest,
			"index_sha256":          digest,
			"output":                map[string]any{"type": "string"},
		},
		"required": []string{"operation", "paths", "previous_index_sha256", "index_sha256", "output"},
	}
	patchFile := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"added":   map[string]any{"type": "integer", "minimum": 0},
			"deleted": map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{"path", "added", "deleted"},
	}
	patchResult := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"files":                 map[string]any{"type": "array", "minItems": 1, "items": patchFile},
			"reverse":               map[string]any{"type": "boolean"},
			"patch_sha256":          digest,
			"previous_index_sha256": digest,
			"index_sha256":          digest,
		},
		"required": []string{"files", "reverse", "patch_sha256", "previous_index_sha256", "index_sha256"},
	}
	tool.Description = "Modify only the Git index with action-specific preconditions. paths and unstage accept multiple paths in one call; patch accepts one multi-file partial-staging diff. Every mutation requires the complete index SHA-256 from git_inspect action=index and returns the resulting index digest."
	tool.InputSchema = schema
	tool.OutputSchema = map[string]any{
		"type": "object", "oneOf": []any{pathResult, patchResult},
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"paths": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_index_sha256"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1000, RecoveryReference: "git_inspect action=index",
		},
		"unstage": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_index_sha256"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1000, RecoveryReference: "git_inspect action=index",
		},
		"patch": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_index_sha256"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1000, RecoveryReference: "git_inspect action=index",
		},
	})
}

func applyGeneratedToolOverrides(definitions []*mcp.Tool) error {
	overrides := generatedToolOverrides()
	for _, definition := range definitions {
		override := overrides[definition.Name]
		if override == nil {
			continue
		}
		if err := override(definition); err != nil {
			return fmt.Errorf("generate contract for %s: %w", definition.Name, err)
		}
		delete(overrides, definition.Name)
	}
	if len(overrides) != 0 {
		names := make([]string, 0, len(overrides))
		for name := range overrides {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("generated tool override targets missing snapshot tools: %s", strings.Join(names, ", "))
	}
	return nil
}
