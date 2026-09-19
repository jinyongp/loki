package service

import (
	"context"
	"encoding/json"
	"errors"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/devtools"
	"loki/internal/rpc"
)

type DevtoolsCoordinationMutator interface {
	MutateCoordination(context.Context, string, devtools.CoordinationMutation, devtools.CoordinationMutationRequest) (devtools.CoordinationMutationResult, error)
}

type devtoolsCoordinationMutationRequest struct {
	Action              string   `json:"action"`
	CWD                 string   `json:"cwd"`
	TargetID            string   `json:"target_id"`
	WorkstreamID        string   `json:"workstream_id"`
	ExpectedRunID       string   `json:"expected_run_id"`
	RequestID           string   `json:"request_id"`
	Context             string   `json:"context"`
	Summary             string   `json:"summary"`
	Decisions           []string `json:"decisions"`
	ValidationRecordIDs []string `json:"validation_record_ids"`
	Remaining           []string `json:"remaining"`
	NextAction          string   `json:"next_action"`
	Blockers            []string `json:"blockers"`
}

func DevtoolsCoordinationMutationOperations(mutator DevtoolsCoordinationMutator) map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"devtools_coordination_mutate": {
			Grant: controlpolicy.Agent,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				if mutator == nil {
					return nil, errors.New("devtools coordination mutation is unavailable")
				}
				request, err := rpc.Decode[devtoolsCoordinationMutationRequest](raw)
				if err != nil {
					return nil, err
				}
				action := devtools.CoordinationMutation(request.Action)
				switch action {
				case devtools.CoordinationClaim, devtools.CoordinationTakeover, devtools.CoordinationResume,
					devtools.CoordinationCheckpoint, devtools.CoordinationRelease, devtools.CoordinationDone:
				default:
					return nil, errors.New("unsupported devtools coordination mutation")
				}
				result, err := mutator.MutateCoordination(ctx, request.CWD, action, devtools.CoordinationMutationRequest{
					RequestID: request.RequestID, Target: request.TargetID, Workstream: request.WorkstreamID,
					ExpectedRun: request.ExpectedRunID, Context: request.Context, Summary: request.Summary,
					Decisions: request.Decisions, ValidationRecordIDs: request.ValidationRecordIDs,
					Remaining: request.Remaining, NextAction: request.NextAction, Blockers: request.Blockers,
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"public": json.RawMessage(result.Public), "context": result.Context, "context_valid": result.ContextValid,
					"run_id": result.RunID, "task_id": result.TaskID, "profile": result.Profile,
				}, nil
			},
		},
	}
}
