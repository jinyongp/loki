package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func githubProviderReadTool() (*mcp.Tool, error) {
	input, err := (ActionInputContract{
		Title:             "github_readArguments",
		ActionDescription: "Typed GitHub repository read operation.",
		Fields: []ActionField{
			{Name: "target", Schema: map[string]any{
				"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$",
				"description": "Configured owner/repository target.",
			}},
			{Name: "number", Schema: map[string]any{
				"type": "integer", "minimum": 1,
				"description": "Issue or pull request number for numbered read actions.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "repository", Required: []string{"target"}},
			{Name: "issue", Required: []string{"target", "number"}},
			{Name: "pull_request", Required: []string{"target", "number"}},
		},
	}).Schema()
	if err != nil {
		return nil, err
	}
	return &mcp.Tool{
		Name:        "github_read",
		Description: "Read typed repository, issue, or pull request metadata from one configured GitHub repository using repository-scoped GitHub App credentials.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true, DestructiveHint: boolPointer(false),
			IdempotentHint: true, OpenWorldHint: boolPointer(true),
		},
		InputSchema: input,
		OutputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"target":       map[string]any{"type": "string"},
				"action":       map[string]any{"type": "string", "enum": []string{"repository", "issue", "pull_request"}},
				"repository":   map[string]any{"type": "object"},
				"issue":        map[string]any{"type": "object"},
				"pull_request": map[string]any{"type": "object"},
			},
			"required": []string{"target", "action"},
		},
	}, nil
}

func githubProviderWriteTool() (*mcp.Tool, error) {
	input, err := (ActionInputContract{
		Title:             "github_writeArguments",
		ActionDescription: "Replay-safe typed GitHub repository mutation.",
		Fields: []ActionField{
			{Name: "target", Schema: map[string]any{
				"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$",
				"description": "Configured owner/repository target.",
			}},
			{Name: "number", Schema: map[string]any{
				"type": "integer", "minimum": 1,
				"description": "Issue or pull request number receiving the comment.",
			}},
			{Name: "body", Schema: map[string]any{
				"type": "string", "minLength": 1, "maxLength": 60000,
				"description": "Comment body. Loki appends a hidden replay marker derived from request_id.",
			}},
			{Name: "request_id", Schema: RequestIDSchema("UUID replay identity. Retry the identical mutation with the same request_id.")},
		},
		Variants: []ActionVariant{
			{Name: "comment", Required: []string{"target", "number", "body", "request_id"}},
		},
	}).Schema()
	if err != nil {
		return nil, err
	}
	tool := &mcp.Tool{
		Name:        "github_write",
		Description: "Create a replay-safe GitHub issue or pull request comment. Loki records a hidden operation marker upstream so identical retries do not create duplicate comments.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: false, DestructiveHint: boolPointer(false),
			IdempotentHint: true, OpenWorldHint: boolPointer(true),
		},
		InputSchema: input,
		OutputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"target":       map[string]any{"type": "string"},
				"number":       map[string]any{"type": "integer"},
				"comment_id":   map[string]any{"type": "integer"},
				"html_url":     map[string]any{"type": "string"},
				"request_id":   map[string]any{"type": "string"},
				"operation_id": map[string]any{"type": "string", "pattern": "^[0-9a-f]{32}$"},
				"replayed":     map[string]any{"type": "boolean"},
			},
			"required": []string{"target", "number", "comment_id", "html_url", "request_id", "operation_id", "replayed"},
		},
	}
	if err := ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"comment": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			FailureAtomicity: FailureUpstream, CrashRecovery: CrashRecoveryUpstream,
			AffectedResourceLimit: 1,
			RecoveryReference:     "github_read action=issue or pull_request, then inspect upstream comments for the Loki operation marker",
		},
	}); err != nil {
		return nil, err
	}
	return tool, nil
}
