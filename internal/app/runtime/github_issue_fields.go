package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	controlpolicy "loki/internal/control/policy"
	githubapp "loki/internal/integrations/github"
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
		return rpc.Operation{Grant: controlpolicy.Agent, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
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
