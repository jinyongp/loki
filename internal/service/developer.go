package service

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/gitops"
	"loki/internal/mcpserver"
	"loki/internal/workspace"
)

type developerRequest struct {
	Action, CWD string
	Staged      bool
	Path        *string
}

func DeveloperHandler(files *workspace.Files, git *gitops.Controller) mcpserver.Handler {
	return mcpserver.Typed(func(ctx context.Context, r developerRequest) (*mcp.CallToolResult, error) {
		var kind, title, subtitle, content string
		var truncated bool
		var stats map[string]any
		switch r.Action {
		case "git_diff":
			diff, err := git.Diff(ctx, r.CWD, r.Staged, r.Path)
			if err != nil {
				return nil, err
			}
			if diff["exit_code"] != 0 {
				return nil, fault.Error("git diff failed")
			}
			content, _ = diff["output"].(string)
			truncated, _ = diff["truncated"].(bool)
			kind, title, subtitle = "diff", "Worktree changes", r.CWD
			if r.Staged {
				title = "Staged changes"
			}
			if r.Path != nil && *r.Path != "" {
				subtitle += " · " + *r.Path
			}
			files, added, removed := 0, 0, 0
			for _, line := range strings.Split(content, "\n") {
				if strings.HasPrefix(line, "diff --git ") {
					files++
				}
				if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
					added++
				}
				if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
					removed++
				}
			}
			stats = map[string]any{"files": files, "additions": added, "deletions": removed}
		case "test_report":
			path, err := mcpserver.Require(r.Path, "path")
			if err != nil {
				return nil, err
			}
			content, stats, err = files.TestReport(path)
			if err != nil {
				return nil, err
			}
			kind, title, subtitle = "test", "Test report", path
		default:
			return nil, fault.Error("developer_view action must be git_diff or test_report")
		}
		result := map[string]any{"kind": kind, "title": title, "subtitle": subtitle, "content": content, "truncated": truncated, "stats": stats}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: title + ": " + subtitle}}, StructuredContent: result}, nil
	})
}
