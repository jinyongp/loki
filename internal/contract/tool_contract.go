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
	Name     string
	Required []string
	Optional []string
}

type ActionInputContract struct {
	Title             string
	ActionDescription string
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
	branches := make([]any, 0, len(c.Variants))
	for _, variant := range c.Variants {
		if variant.Name == "" || seenActions[variant.Name] {
			return nil, errors.New("action contract has an empty or duplicate variant")
		}
		seenActions[variant.Name] = true
		actionNames = append(actionNames, variant.Name)

		allowed := make(map[string]bool, len(variant.Required)+len(variant.Optional))
		required := []string{"action"}
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
			properties[name] = cloned
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
	sort.Strings(actionNames)

	rootProperties := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        actionNames,
			"description": c.ActionDescription,
		},
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

	return map[string]any{
		"type":                 "object",
		"title":                c.Title,
		"additionalProperties": false,
		"properties":           rootProperties,
		"required":             []string{"action"},
		"oneOf":                branches,
	}, nil
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
	return &mcp.Tool{
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
	}, nil
}

func generatedToolOverrides() map[string]toolOverride {
	return map[string]toolOverride{
		"workspace_edit": overrideWorkspaceEdit,
		"git_stage":      overrideGitStage,
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
		},
		Variants: []ActionVariant{
			{Name: "create", Required: []string{"path", "content"}},
			{Name: "replace", Required: []string{"path", "old", "new", "expected_sha256", "expected_replacements"}},
			{Name: "patch", Required: []string{"patch"}},
			{Name: "move", Required: []string{"source", "destination"}},
		},
	}).Schema()
	if err != nil {
		return err
	}
	tool.Description = "Edit workspace text with action-specific preconditions. create, replace, and move operate on one path; patch applies one preflighted unified diff across multiple files up to the configured patch-file limit."
	tool.InputSchema = schema
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
	tool.Description = "Modify only the Git index with action-specific preconditions. paths and unstage accept multiple paths in one call; patch accepts one multi-file partial-staging diff. Every mutation requires the index SHA-256 from git_inspect action=index."
	tool.InputSchema = schema
	return nil
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
