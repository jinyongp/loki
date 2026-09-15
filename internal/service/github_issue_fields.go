package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/githubapp"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
)

type IssueFieldsClient interface {
	ListFields(context.Context, string) ([]githubapp.IssueField, error)
	ListValues(context.Context, string, int64) ([]githubapp.IssueFieldValue, error)
	AddValues(context.Context, string, int64, []githubapp.Value) ([]githubapp.IssueFieldValue, error)
	SetValues(context.Context, string, int64, []githubapp.Value) ([]githubapp.IssueFieldValue, error)
	ClearValue(context.Context, string, int64, int64) error
}

type githubRequest struct {
	Operation string            `json:"operation"`
	Target    string            `json:"target"`
	Issue     int64             `json:"issue"`
	FieldID   int64             `json:"field_id"`
	Values    []githubapp.Value `json:"values"`
}

func GitHubIssueFieldsOperations(client IssueFieldsClient) map[string]rpc.Operation {
	call := func(handler func(context.Context, githubRequest) (map[string]any, error)) rpc.Operation {
		return rpc.Operation{Permission: rpc.Agent, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var r githubRequest
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&r) != nil {
				return nil, errors.New("invalid GitHub Issue Fields arguments")
			}
			if client == nil {
				return nil, errors.New("GitHub Issue Fields is not configured")
			}
			return handler(ctx, r)
		}}
	}
	return map[string]rpc.Operation{
		"github_fields_list": call(func(ctx context.Context, r githubRequest) (map[string]any, error) {
			values, err := client.ListFields(ctx, r.Target)
			return map[string]any{"fields": values}, err
		}),
		"github_values_list": call(func(ctx context.Context, r githubRequest) (map[string]any, error) {
			values, err := client.ListValues(ctx, r.Target, r.Issue)
			return map[string]any{"values": values}, err
		}),
		"github_values_add": call(func(ctx context.Context, r githubRequest) (map[string]any, error) {
			values, err := client.AddValues(ctx, r.Target, r.Issue, r.Values)
			return map[string]any{"values": values}, err
		}),
		"github_values_set": call(func(ctx context.Context, r githubRequest) (map[string]any, error) {
			values, err := client.SetValues(ctx, r.Target, r.Issue, r.Values)
			return map[string]any{"values": values}, err
		}),
		"github_values_clear": call(func(ctx context.Context, r githubRequest) (map[string]any, error) {
			err := client.ClearValue(ctx, r.Target, r.Issue, r.FieldID)
			return map[string]any{"cleared": err == nil}, err
		}),
	}
}

type githubMCPRequest struct {
	Action  string            `json:"action"`
	Target  string            `json:"target"`
	Issue   int64             `json:"issue"`
	FieldID int64             `json:"field_id"`
	Values  []githubapp.Value `json:"values"`
}

func GitHubIssueFieldsHandlers(runtime RuntimeCaller) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{"github_issue_fields": mcpserver.Typed(func(ctx context.Context, r githubMCPRequest) (*mcp.CallToolResult, error) {
		operations := map[string]string{"list_fields": "github_fields_list", "list_values": "github_values_list", "add_values": "github_values_add", "set_values": "github_values_set", "clear_value": "github_values_clear"}
		operation, ok := operations[r.Action]
		if !ok {
			return nil, errors.New("GitHub Issue Fields action is invalid")
		}
		request := map[string]any{"operation": operation, "target": r.Target}
		switch r.Action {
		case "list_values":
			request["issue"] = r.Issue
		case "add_values", "set_values":
			request["issue"], request["values"] = r.Issue, r.Values
		case "clear_value":
			request["issue"], request["field_id"] = r.Issue, r.FieldID
		}
		return runtimeObject(ctx, runtime, request)
	})}
}
