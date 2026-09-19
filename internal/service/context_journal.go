package service

import (
	"context"
	"encoding/json"
	"errors"

	"loki/internal/agentcontext"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/rpc"
)

type contextCheckpointPutRequest struct {
	RequestID        string                    `json:"request_id"`
	ExpectedPrevious string                    `json:"expected_previous"`
	Draft            agentcontext.ContextDraft `json:"draft"`
}

type contextCheckpointScopeRequest struct {
	RepositoryID string `json:"repository_id"`
	WorktreeID   string `json:"worktree_id"`
	WorkstreamID string `json:"workstream_id"`
	RecordID     string `json:"record_id"`
	Limit        int    `json:"limit"`
}

func contextScope(request contextCheckpointScopeRequest) agentcontext.ContextScope {
	return agentcontext.ContextScope{
		RepositoryID: request.RepositoryID,
		WorktreeID:   request.WorktreeID,
		WorkstreamID: request.WorkstreamID,
	}
}

func ContextJournalOperations(journal *agentcontext.ContextJournal) map[string]rpc.Operation {
	decodePut := func(ctx context.Context, raw json.RawMessage) (any, error) {
		if journal == nil {
			return nil, errors.New("context journal is unavailable")
		}
		request, err := rpc.Decode[contextCheckpointPutRequest](raw)
		if err != nil {
			return nil, err
		}
		return journal.Put(ctx, request.RequestID, request.ExpectedPrevious, request.Draft)
	}
	decodeScope := func(handler func(context.Context, contextCheckpointScopeRequest) (any, error)) rpc.Operation {
		return rpc.Operation{Grant: controlpolicy.Agent, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if journal == nil {
				return nil, errors.New("context journal is unavailable")
			}
			request, err := rpc.Decode[contextCheckpointScopeRequest](raw)
			if err != nil {
				return nil, err
			}
			return handler(ctx, request)
		}}
	}
	return map[string]rpc.Operation{
		"context_checkpoint_put": {
			Grant:  controlpolicy.Agent,
			Handle: decodePut,
		},
		"context_checkpoint_latest": decodeScope(func(ctx context.Context, request contextCheckpointScopeRequest) (any, error) {
			if request.RecordID != "" || request.Limit != 0 {
				return nil, errors.New("latest context checkpoint does not accept record_id or limit")
			}
			return journal.Latest(ctx, contextScope(request))
		}),
		"context_checkpoint_get": decodeScope(func(ctx context.Context, request contextCheckpointScopeRequest) (any, error) {
			if request.RecordID == "" || request.Limit != 0 {
				return nil, errors.New("context checkpoint get requires record_id and does not accept limit")
			}
			return journal.Get(ctx, contextScope(request), request.RecordID)
		}),
		"context_checkpoint_list": decodeScope(func(ctx context.Context, request contextCheckpointScopeRequest) (any, error) {
			if request.RecordID != "" {
				return nil, errors.New("context checkpoint list does not accept record_id")
			}
			return journal.List(ctx, contextScope(request), request.Limit)
		}),
	}
}
