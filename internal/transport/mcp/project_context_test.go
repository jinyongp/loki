package mcptransport

import (
	"context"
	"encoding/json"
	"testing"
)

func TestProjectContextCoordinationChoosesClaimAndDetectsAmbiguity(t *testing.T) {
	const (
		taskA        = "11111111-1111-4111-8111-111111111111"
		taskB        = "22222222-2222-4222-8222-222222222222"
		taskC        = "66666666-6666-4666-8666-666666666666"
		runA         = "33333333-3333-4333-8333-333333333333"
		runB         = "44444444-4444-4444-8444-444444444444"
		workstreamID = "55555555-5555-4555-8555-555555555555"
	)
	t.Run("claim next work", func(t *testing.T) {
		runtime := runtimeFixture(func(_ context.Context, request any) (json.RawMessage, error) {
			row := request.(map[string]any)
			switch row["operation"] {
			case "devtools_task_current":
				raw, _ := json.Marshal(map[string]any{"profile": "fixture", "revision": 1, "items": []any{}})
				return raw, nil
			case "devtools_task_next":
				raw, _ := json.Marshal(map[string]any{
					"profile": "fixture", "revision": 2,
					"item": map[string]any{"id": taskA, "workstream_id": workstreamID},
				})
				return raw, nil
			case "devtools_task_context":
				raw, _ := json.Marshal(map[string]any{
					"profile": "fixture", "revision": 3,
					"item":        map[string]any{"id": taskA, "workstream_id": workstreamID},
					"validations": []any{}, "history": []any{}, "tasks": []any{}, "omitted_ids": []string{},
				})
				return raw, nil
			default:
				t.Fatalf("unexpected operation: %#v", row)
				return nil, nil
			}
		})
		controller := &ProjectContextController{Runtime: runtime, Claims: NewDevtoolsSessionClaims()}
		_, taskID, runID, gotWorkstream, transition, ambiguous, _, gaps, _, err :=
			controller.resolveCoordination(t.Context(), "repo", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if ambiguous || taskID != taskA || runID != "" || gotWorkstream != workstreamID ||
			transition.Action != "claim" || len(gaps) != 0 {
			t.Fatalf("claim resolution task=%s run=%s workstream=%s transition=%#v ambiguous=%v gaps=%#v",
				taskID, runID, gotWorkstream, transition, ambiguous, gaps)
		}
	})

	t.Run("multiple current runs are ambiguous unless task disambiguates", func(t *testing.T) {
		runtime := runtimeFixture(func(_ context.Context, request any) (json.RawMessage, error) {
			row := request.(map[string]any)
			switch row["operation"] {
			case "devtools_task_current":
				raw, _ := json.Marshal(map[string]any{
					"profile": "fixture", "revision": 4,
					"items": []any{
						map[string]any{"id": runA, "task_id": taskA, "workstream_id": workstreamID},
						map[string]any{"id": runB, "task_id": taskB, "workstream_id": workstreamID},
					},
				})
				return raw, nil
			case "devtools_task_context":
				raw, _ := json.Marshal(map[string]any{
					"profile": "fixture", "revision": 5,
					"item":        map[string]any{"id": row["task_id"], "workstream_id": workstreamID},
					"validations": []any{}, "history": []any{}, "tasks": []any{}, "omitted_ids": []string{},
				})
				return raw, nil
			default:
				t.Fatalf("unexpected operation: %#v", row)
				return nil, nil
			}
		})
		controller := &ProjectContextController{Runtime: runtime, Claims: NewDevtoolsSessionClaims()}
		_, _, _, _, transition, ambiguous, _, gaps, _, err :=
			controller.resolveCoordination(t.Context(), "repo", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if !ambiguous || transition.Action != "inspect" {
			t.Fatalf("ambiguous resolution transition=%#v ambiguous=%v gaps=%#v", transition, ambiguous, gaps)
		}
		found := false
		for _, gap := range gaps {
			if gap == "coordination.current.ambiguous" {
				found = true
			}
		}
		if !found {
			t.Fatalf("ambiguous gap missing: %#v", gaps)
		}

		_, taskID, runID, _, disambiguatedTransition, disambiguatedAmbiguous, _, _, _, err :=
			controller.resolveCoordination(t.Context(), "repo", taskA, "")
		if err != nil {
			t.Fatal(err)
		}
		if disambiguatedAmbiguous || taskID != taskA || runID != runA || disambiguatedTransition.Action != "takeover" {
			t.Fatalf("disambiguated resolution task=%s run=%s transition=%#v ambiguous=%v",
				taskID, runID, disambiguatedTransition, disambiguatedAmbiguous)
		}

		_, _, _, _, mismatchTransition, mismatchAmbiguous, _, mismatchGaps, _, err :=
			controller.resolveCoordination(t.Context(), "repo", taskC, "")
		if err != nil {
			t.Fatal(err)
		}
		if !mismatchAmbiguous || mismatchTransition.Action != "inspect" {
			t.Fatalf("mismatched explicit task transition=%#v ambiguous=%v gaps=%#v", mismatchTransition, mismatchAmbiguous, mismatchGaps)
		}
		foundMismatch := false
		for _, gap := range mismatchGaps {
			if gap == "coordination.current.task_mismatch" {
				foundMismatch = true
			}
		}
		if !foundMismatch {
			t.Fatalf("task mismatch gap missing: %#v", mismatchGaps)
		}
	})
}
