package service

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/workspace"
)

type developerRequest struct {
	Action     string
	CWD        string
	Staged     bool
	Path       *string
	ReportPath *string `json:"report_path"`
	Content    *string
	Truncated  *bool
}

func diffStats(content string) map[string]any {
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
	return map[string]any{"files": files, "additions": added, "deletions": removed}
}

func DeveloperHandler(files *workspace.Files) mcpserver.Handler {
	return mcpserver.Typed(func(_ context.Context, r developerRequest) (*mcp.CallToolResult, error) {
		var kind, title, subtitle, content string
		var truncated bool
		var stats map[string]any
		switch r.Action {
		case "git_diff":
			captured, err := mcpserver.Require(r.Content, "content")
			if err != nil {
				return nil, err
			}
			wasTruncated, err := mcpserver.Require(r.Truncated, "truncated")
			if err != nil {
				return nil, err
			}
			content, truncated = captured, wasTruncated
			kind, title, subtitle = "diff", "Worktree changes", r.CWD
			if r.Staged {
				title = "Staged changes"
			}
			if r.Path != nil && *r.Path != "" {
				subtitle += " · " + *r.Path
			}
			stats = diffStats(content)
		case "test_report":
			path, err := mcpserver.Require(r.ReportPath, "report_path")
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
