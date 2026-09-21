package workspace

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/mcpserver"
)

type gitInspect struct {
	Action, CWD string
	Staged      bool
	Path        *string
}
type gitStage struct {
	Action, CWD string
	Paths       *[]string
	Patch       *string
	Reverse     bool
	Expected    *string `json:"expected_index_sha256"`
}

type Repository interface {
	Status(context.Context, string) (map[string]any, error)
	Diff(context.Context, string, bool, *string) (map[string]any, error)
	Index(context.Context, string) (map[string]any, error)
	CommitContext(context.Context, string) (map[string]any, error)
	MutatePaths(context.Context, string, string, []string, *string) (map[string]any, error)
	StagePatch(context.Context, string, string, bool, *string) (map[string]any, error)
}

func GitHandlers(repository Repository) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"git_inspect": mcpserver.Typed(func(ctx context.Context, r gitInspect) (*mcp.CallToolResult, error) {
			action := r.Action
			if action == "" {
				action = "status"
			}
			switch action {
			case "status":
				return objectResult(repository.Status(ctx, r.CWD))
			case "diff":
				return objectResult(repository.Diff(ctx, r.CWD, r.Staged, r.Path))
			case "index":
				return objectResult(repository.Index(ctx, r.CWD))
			case "commit_context":
				return objectResult(repository.CommitContext(ctx, r.CWD))
			default:
				return nil, fault.Error("git_inspect action must be status, diff, index, or commit_context")
			}
		}),
		"git_stage": mcpserver.Typed(func(ctx context.Context, r gitStage) (*mcp.CallToolResult, error) {
			switch r.Action {
			case "paths", "unstage":
				paths, err := mcpserver.Require(r.Paths, "paths")
				if err != nil {
					return nil, err
				}
				expected, err := mcpserver.Require(r.Expected, "expected_index_sha256")
				if err != nil {
					return nil, err
				}
				op := "stage"
				if r.Action == "unstage" {
					op = "unstage"
				}
				return objectResult(repository.MutatePaths(ctx, op, r.CWD, paths, &expected))
			case "patch":
				patch, err := mcpserver.Require(r.Patch, "patch")
				if err != nil {
					return nil, err
				}
				expected, err := mcpserver.Require(r.Expected, "expected_index_sha256")
				if err != nil {
					return nil, err
				}
				return objectResult(repository.StagePatch(ctx, r.CWD, patch, r.Reverse, &expected))
			default:
				return nil, fault.Error("git_stage action must be paths, unstage, or patch")
			}
		}),
	}
}
