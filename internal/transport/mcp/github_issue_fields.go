package mcptransport

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	githubapp "loki/internal/integrations/github"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
)

type githubMCPRequest struct {
	Action  string            `json:"action"`
	Target  string            `json:"target"`
	Issue   int64             `json:"issue"`
	FieldID int64             `json:"field_id"`
	Values  []githubapp.Value `json:"values"`
}

func GitHubIssueFieldsHandlers(runtime rpc.Caller) map[string]mcpserver.Handler {
	read := mcpserver.Typed(func(ctx context.Context, r githubMCPRequest) (*mcp.CallToolResult, error) {
		operation := map[string]string{
			"list_fields": "github_fields_list",
			"list_values": "github_values_list",
		}[r.Action]
		if operation == "" {
			return nil, errors.New("GitHub Issue Fields read action is invalid")
		}
		request := map[string]any{"operation": operation, "target": r.Target}
		if r.Action == "list_values" {
			request["issue"] = r.Issue
		}
		return runtimeObject(ctx, runtime, request)
	})
	write := mcpserver.Typed(func(ctx context.Context, r githubMCPRequest) (*mcp.CallToolResult, error) {
		operation := map[string]string{
			"add_values":  "github_values_add",
			"set_values":  "github_values_set",
			"clear_value": "github_values_clear",
		}[r.Action]
		if operation == "" {
			return nil, errors.New("GitHub Issue Fields write action is invalid")
		}
		request := map[string]any{"operation": operation, "target": r.Target, "issue": r.Issue}
		switch r.Action {
		case "add_values", "set_values":
			request["values"] = r.Values
		case "clear_value":
			request["field_id"] = r.FieldID
		}
		return runtimeObject(ctx, runtime, request)
	})
	return map[string]mcpserver.Handler{
		"github_issue_fields_read":  read,
		"github_issue_fields_write": write,
	}
}
