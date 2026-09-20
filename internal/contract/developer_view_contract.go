package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func developerDiffStatsSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"files":     map[string]any{"type": "integer", "minimum": 0},
			"additions": map[string]any{"type": "integer", "minimum": 0},
			"deletions": map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{"files", "additions", "deletions"},
	}
}

func developerTestStatsSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"format": map[string]any{"type": "string", "minLength": 1}},
				"required":   []string{"format"},
			},
			map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"format":   map[string]any{"const": "junit"},
					"tests":    map[string]any{"type": "integer", "minimum": 0},
					"failures": map[string]any{"type": "integer", "minimum": 0},
					"errors":   map[string]any{"type": "integer", "minimum": 0},
					"skipped":  map[string]any{"type": "integer", "minimum": 0},
				},
				"required": []string{"format", "tests", "failures", "errors", "skipped"},
			},
		},
	}
}

func overrideDeveloperView(tool *mcp.Tool) error {
	input, err := (ActionInputContract{
		Title:             "developer_viewArguments",
		ActionDescription: "Developer-output presentation mode.",
		Fields: []ActionField{
			{Name: "cwd", Schema: map[string]any{
				"type": "string", "minLength": 1, "maxLength": 4096, "default": ".",
				"description": "Repository directory label captured with a git_inspect action=diff result; developer_view does not inspect Git itself.",
			}},
			{Name: "path", Schema: map[string]any{
				"type": "string", "minLength": 1, "maxLength": 4096,
				"description": "Optional repository-relative path label captured with the Git diff.",
			}},
			{Name: "staged", Schema: map[string]any{
				"type": "boolean", "default": false,
				"description": "Whether the captured Git diff represents staged index changes.",
			}},
			{Name: "content", Schema: map[string]any{
				"type": "string", "maxLength": 16777216,
				"description": "Captured bounded diff output from git_inspect action=diff. Empty content is valid when no changes were present.",
			}},
			{Name: "truncated", Schema: map[string]any{
				"type":        "boolean",
				"description": "Truncation flag from the same captured git_inspect diff result.",
			}},
			{Name: "report_path", Schema: map[string]any{
				"type": "string", "minLength": 1, "maxLength": 4096,
				"description": "Workspace-relative saved test-report path to read and render.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "git_diff", Required: []string{"content", "truncated"}, Optional: []string{"cwd", "path", "staged"}},
			{Name: "test_report", Required: []string{"report_path"}},
		},
	}).Schema()
	if err != nil {
		return err
	}

	diffOutput := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"kind":      map[string]any{"const": "diff"},
			"title":     map[string]any{"type": "string", "enum": []string{"Worktree changes", "Staged changes"}},
			"subtitle":  map[string]any{"type": "string"},
			"content":   map[string]any{"type": "string", "maxLength": 16777216},
			"truncated": map[string]any{"type": "boolean"},
			"stats":     developerDiffStatsSchema(),
		},
		"required": []string{"kind", "title", "subtitle", "content", "truncated", "stats"},
	}
	testOutput := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"kind":      map[string]any{"const": "test"},
			"title":     map[string]any{"const": "Test report"},
			"subtitle":  map[string]any{"type": "string"},
			"content":   map[string]any{"type": "string", "maxLength": 1048576},
			"truncated": map[string]any{"const": false},
			"stats":     developerTestStatsSchema(),
		},
		"required": []string{"kind", "title", "subtitle", "content", "truncated", "stats"},
	}

	tool.Description = "Render already-captured Git diff output or a saved test-report artifact. The Git variant is presentation-only and never reruns repository inspection."
	tool.InputSchema = input
	tool.OutputSchema = map[string]any{"type": "object", "oneOf": []any{diffOutput, testOutput}}
	if tool.Annotations != nil {
		tool.Annotations.ReadOnlyHint = true
		tool.Annotations.DestructiveHint = boolPointer(false)
		tool.Annotations.IdempotentHint = true
		tool.Annotations.OpenWorldHint = boolPointer(false)
	}
	return nil
}
