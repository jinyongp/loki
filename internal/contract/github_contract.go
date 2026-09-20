package contract

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/githubapp"
)

func overrideGitHub(tool *mcp.Tool) error {
	capabilities := githubapp.RepositoryCommandCapabilities()
	commandGroups := append([]string(nil), capabilities.CommandGroups...)
	tool.Description = "Run a constrained GitHub CLI escape-hatch command with a short-lived installation token issued for one configured repository. Prefer typed GitHub tools when available; this surface may mutate upstream state and is not replay-safe."
	tool.InputSchema = map[string]any{
		"type": "object", "title": "githubArguments", "additionalProperties": false,
		"properties": map[string]any{
			"target": map[string]any{
				"type":        "string",
				"pattern":     "^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$",
				"description": "Configured owner/repository target used to issue a repository-limited GitHub App installation token.",
			},
			"command": map[string]any{
				"type": "string", "enum": commandGroups,
				"description": "Allowed top-level gh command group. Repository/host/organization scope override flags are rejected by the runtime.",
			},
			"args": map[string]any{
				"type": "array", "maxItems": capabilities.MaxArguments - 1,
				"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": capabilities.MaxArgumentBytes},
				"default":     []any{},
				"description": "Arguments after the top-level command. The runtime additionally enforces a total argument-byte bound and rejects scope override flags.",
			},
			"input": map[string]any{
				"type": "string", "default": "", "maxLength": capabilities.MaxInputBytes,
				"description": "Optional bounded stdin text passed to gh. Credentials are injected separately and never accepted through this field.",
			},
		},
		"required": []string{"target", "command"},
	}
	tool.OutputSchema = map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"exit_code": map[string]any{"type": "integer"},
			"output":    map[string]any{"type": "string"},
			"truncated": map[string]any{"type": "boolean"},
			"timed_out": map[string]any{"type": "boolean"},
			"canceled":  map[string]any{"type": "boolean"},
		},
		"required": []string{"exit_code", "output", "truncated", "timed_out", "canceled"},
	}
	tool.Annotations = &mcp.ToolAnnotations{
		ReadOnlyHint: false, DestructiveHint: boolPointer(true),
		IdempotentHint: false, OpenWorldHint: boolPointer(true),
	}
	if tool.Meta == nil {
		tool.Meta = mcp.Meta{}
	}
	tool.Meta["loki/github_command"] = map[string]any{
		"command_groups":        append([]string(nil), capabilities.CommandGroups...),
		"search_subcommands":    append([]string(nil), capabilities.SearchSubcommands...),
		"prohibited_flags":      append([]string(nil), capabilities.ProhibitedFlags...),
		"max_arguments":         capabilities.MaxArguments,
		"max_argument_bytes":    capabilities.MaxArgumentBytes,
		"max_argument_total":    capabilities.MaxArgumentTotal,
		"max_input_bytes":       capabilities.MaxInputBytes,
		"repository_token_only": capabilities.RepositoryTokenOnly,
		"preferred_surface":     "typed GitHub tools when available",
	}
	return ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"command": {
			Replay: ReplayUnsafe, FailureAtomicity: FailureUpstream,
			CrashRecovery: CrashRecoveryUpstream, AffectedResourceLimit: 1,
			RecoveryReference: "inspect the configured GitHub repository state before deciding whether to retry",
		},
	})
}
