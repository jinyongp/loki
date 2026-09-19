package contract

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ContractIssue struct {
	Tool  string `json:"tool"`
	Code  string `json:"code"`
	Field string `json:"field,omitempty"`
}

type ContractAudit struct {
	ToolCount              int             `json:"tool_count"`
	ActionUnionCount       int             `json:"action_union_count"`
	FlatActionUnionCount   int             `json:"flat_action_union_count"`
	InputPropertyCount     int             `json:"input_property_count"`
	DescribedPropertyCount int             `json:"described_property_count"`
	MissingOutputSchema    int             `json:"missing_output_schema"`
	OpenOutputSchema       int             `json:"open_output_schema"`
	MissingAnnotations     int             `json:"missing_annotations"`
	Issues                 []ContractIssue `json:"issues"`
}

func schemaMap(value any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	return schema, nil
}

func actionEnum(property any) []any {
	schema, _ := property.(map[string]any)
	values, _ := schema["enum"].([]any)
	return values
}

func AuditDefinitions(definitions []*mcp.Tool) (ContractAudit, error) {
	audit := ContractAudit{ToolCount: len(definitions), Issues: []ContractIssue{}}
	for _, definition := range definitions {
		if definition == nil || definition.Name == "" {
			return ContractAudit{}, fmt.Errorf("audit encountered invalid tool definition")
		}
		input, err := schemaMap(definition.InputSchema)
		if err != nil {
			return ContractAudit{}, fmt.Errorf("decode input schema for %s: %w", definition.Name, err)
		}
		properties, _ := input["properties"].(map[string]any)
		names := make([]string, 0, len(properties))
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)
		audit.InputPropertyCount += len(names)
		for _, name := range names {
			property, _ := properties[name].(map[string]any)
			description, _ := property["description"].(string)
			if strings.TrimSpace(description) == "" {
				audit.Issues = append(audit.Issues, ContractIssue{
					Tool: definition.Name, Code: "missing_field_description", Field: name,
				})
			} else {
				audit.DescribedPropertyCount++
			}
		}
		if len(actionEnum(properties["action"])) != 0 {
			audit.ActionUnionCount++
			oneOf, _ := input["oneOf"].([]any)
			anyOf, _ := input["anyOf"].([]any)
			if len(oneOf) == 0 && len(anyOf) == 0 {
				audit.FlatActionUnionCount++
				audit.Issues = append(audit.Issues, ContractIssue{
					Tool: definition.Name, Code: "flat_action_union",
				})
			}
		}

		if definition.OutputSchema == nil {
			audit.MissingOutputSchema++
			audit.Issues = append(audit.Issues, ContractIssue{
				Tool: definition.Name, Code: "missing_output_schema",
			})
		} else {
			output, err := schemaMap(definition.OutputSchema)
			if err != nil {
				return ContractAudit{}, fmt.Errorf("decode output schema for %s: %w", definition.Name, err)
			}
			if open, _ := output["additionalProperties"].(bool); open {
				audit.OpenOutputSchema++
				audit.Issues = append(audit.Issues, ContractIssue{
					Tool: definition.Name, Code: "open_output_schema",
				})
			}
		}
		if definition.Annotations == nil {
			audit.MissingAnnotations++
			audit.Issues = append(audit.Issues, ContractIssue{
				Tool: definition.Name, Code: "missing_annotations",
			})
		}
	}
	sort.Slice(audit.Issues, func(i, j int) bool {
		if audit.Issues[i].Tool == audit.Issues[j].Tool {
			if audit.Issues[i].Code == audit.Issues[j].Code {
				return audit.Issues[i].Field < audit.Issues[j].Field
			}
			return audit.Issues[i].Code < audit.Issues[j].Code
		}
		return audit.Issues[i].Tool < audit.Issues[j].Tool
	})
	return audit, nil
}
