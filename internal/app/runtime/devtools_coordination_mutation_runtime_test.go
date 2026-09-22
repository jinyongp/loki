package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"loki/internal/devtools"
)

type recordingCoordinationMutator struct {
	action  devtools.CoordinationMutation
	request devtools.CoordinationMutationRequest
	cwd     string
	result  devtools.CoordinationMutationResult
	err     error
}

func (r *recordingCoordinationMutator) MutateCoordination(
	_ context.Context,
	cwd string,
	action devtools.CoordinationMutation,
	request devtools.CoordinationMutationRequest,
) (devtools.CoordinationMutationResult, error) {
	r.cwd, r.action, r.request = cwd, action, request
	return r.result, r.err
}

func TestDevtoolsCoordinationMutationRuntimeOperationIsFixedAndTyped(t *testing.T) {
	const (
		privateContext = "session-private-context-canary"
		taskID         = "11111111-1111-4111-8111-111111111111"
		runID          = "22222222-2222-4222-8222-222222222222"
	)
	mutator := &recordingCoordinationMutator{result: devtools.CoordinationMutationResult{
		Public: json.RawMessage(`{"profile":"fixture","claimed":true}`), Profile: "fixture",
		Context: privateContext, ContextValid: true, RunID: runID, TaskID: taskID,
	}}
	op := DevtoolsCoordinationMutationOperations(mutator)["devtools_coordination_mutate"]
	result, err := op.Handle(t.Context(), json.RawMessage(`{
		"operation":"devtools_coordination_mutate",
		"action":"task claim",
		"cwd":"repo",
		"target_id":"11111111-1111-4111-8111-111111111111",
		"request_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if mutator.action != devtools.CoordinationClaim || mutator.cwd != "repo" || mutator.request.Target != taskID {
		t.Fatalf("runtime mutation = %#v", mutator)
	}
	row := result.(map[string]any)
	if row["context"] != privateContext || row["run_id"] != runID || row["task_id"] != taskID {
		t.Fatalf("runtime private result = %#v", row)
	}
	for _, raw := range []string{
		`{"operation":"devtools_coordination_mutate","action":"task add","request_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}`,
		`{"operation":"devtools_coordination_mutate","action":"task claim","request_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","profile":"other"}`,
	} {
		mutator.action = ""
		if _, err := op.Handle(t.Context(), json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe runtime mutation accepted: %s", raw)
		}
		if mutator.action != "" {
			t.Fatal("mutator ran for rejected runtime request")
		}
	}
}
