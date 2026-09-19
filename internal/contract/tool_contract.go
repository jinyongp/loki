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

func generatedToolOverrides() map[string]toolOverride {
	return map[string]toolOverride{
		"workspace_edit": overrideWorkspaceEdit,
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
