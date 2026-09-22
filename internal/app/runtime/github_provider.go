package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	controlpolicy "loki/internal/control/policy"
	githubapp "loki/internal/integrations/github"
	"loki/internal/rpc"
)

type GitHubProvider interface {
	Read(context.Context, githubapp.ProviderReadRequest) (githubapp.ProviderReadResult, error)
	Comment(context.Context, githubapp.CommentRequest) (githubapp.CommentResult, error)
}

type githubProviderRPCRequest struct {
	Operation string `json:"operation"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Number    int64  `json:"number"`
	Body      string `json:"body"`
	RequestID string `json:"request_id"`
}

func GitHubProviderOperations(provider GitHubProvider) map[string]rpc.Operation {
	decode := func(raw json.RawMessage) (githubProviderRPCRequest, error) {
		var request githubProviderRPCRequest
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil {
			return githubProviderRPCRequest{}, errors.New("invalid GitHub provider arguments")
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return githubProviderRPCRequest{}, errors.New("invalid GitHub provider arguments")
		}
		if provider == nil {
			return githubProviderRPCRequest{}, errors.New("GitHub provider is not configured")
		}
		return request, nil
	}
	return map[string]rpc.Operation{
		"github_provider_read": {
			Grant: controlpolicy.Agent,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := decode(raw)
				if err != nil {
					return nil, err
				}
				if request.Operation != "github_provider_read" {
					return nil, errors.New("GitHub provider read operation is invalid")
				}
				result, err := provider.Read(ctx, githubapp.ProviderReadRequest{
					Target: request.Target, Action: githubapp.ProviderReadAction(request.Action), Number: request.Number,
				})
				if err != nil {
					return nil, err
				}
				return githubReadResult(result), nil
			},
		},
		"github_provider_comment": {
			Grant: controlpolicy.Agent,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := decode(raw)
				if err != nil {
					return nil, err
				}
				if request.Operation != "github_provider_comment" || request.Action != "comment" {
					return nil, errors.New("GitHub provider write operation is invalid")
				}
				result, err := provider.Comment(ctx, githubapp.CommentRequest{
					Target: request.Target, Number: request.Number, Body: request.Body, RequestID: request.RequestID,
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"target": result.Target, "number": result.Number, "comment_id": result.CommentID,
					"html_url": result.HTMLURL, "request_id": result.RequestID,
					"operation_id": result.OperationID, "replayed": result.Replayed,
				}, nil
			},
		},
	}
}

func githubReadResult(result githubapp.ProviderReadResult) map[string]any {
	value := map[string]any{"target": result.Target, "action": string(result.Action)}
	if result.Repository != nil {
		value["repository"] = result.Repository
	}
	if result.Issue != nil {
		value["issue"] = result.Issue
	}
	if result.PullRequest != nil {
		value["pull_request"] = result.PullRequest
	}
	return value
}
