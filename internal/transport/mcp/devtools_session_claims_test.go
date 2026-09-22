package mcptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"loki/internal/devtools"
)

const (
	sessionClaimContext = "session-private-context-canary"
	sessionTaskID       = "11111111-1111-4111-8111-111111111111"
	sessionRunID        = "22222222-2222-4222-8222-222222222222"
	sessionOtherRunID   = "33333333-3333-4333-8333-333333333333"
)

func TestDevtoolsSessionCoordinationKeepsContextPrivateAndSessionScoped(t *testing.T) {
	calls := 0
	var last map[string]any
	runtime := runtimeFixture(func(_ context.Context, request any) (json.RawMessage, error) {
		calls++
		last = request.(map[string]any)
		action := last["action"].(string)
		public := map[string]any{
			"profile": "fixture", "revision": 2, "request_id": last["request_id"], "changed": true,
		}
		response := map[string]any{"public": public, "profile": "fixture"}
		switch action {
		case string(devtools.CoordinationClaim), string(devtools.CoordinationTakeover):
			public["claimed"] = true
			response["context"] = sessionClaimContext
			response["context_valid"] = true
			response["run_id"] = sessionRunID
			response["task_id"] = sessionTaskID
		case string(devtools.CoordinationCheckpoint), string(devtools.CoordinationResume):
			response["context_valid"] = true
		case string(devtools.CoordinationRelease), string(devtools.CoordinationDone):
			response["context_valid"] = false
		}
		raw, _ := json.Marshal(response)
		return raw, nil
	})
	controller := &DevtoolsSessionCoordination{Runtime: runtime, Claims: NewDevtoolsSessionClaims()}
	public, err := controller.Mutate(t.Context(), "session-a", devtools.CoordinationClaim, DevtoolsSessionMutationRequest{
		CWD: ".", TargetID: sessionTaskID, RequestID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(public)
	if strings.Contains(string(encoded), sessionClaimContext) {
		t.Fatalf("claim context leaked: %s", encoded)
	}
	binding, ok := controller.Claims.get("session-a")
	if !ok || binding.context != sessionClaimContext || binding.RunID != sessionRunID || binding.TaskID != sessionTaskID {
		t.Fatalf("stored binding = %#v %v", binding, ok)
	}

	before := calls
	if _, err := controller.Mutate(t.Context(), "session-b", devtools.CoordinationCheckpoint, DevtoolsSessionMutationRequest{
		TargetID: sessionRunID, RequestID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Summary: "progress",
	}); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("cross-session checkpoint error = %v", err)
	}
	if calls != before {
		t.Fatal("runtime was called for cross-session ownership failure")
	}

	if _, err := controller.Mutate(t.Context(), "session-a", devtools.CoordinationCheckpoint, DevtoolsSessionMutationRequest{
		TargetID: sessionRunID, RequestID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Summary: "progress",
	}); err != nil {
		t.Fatal(err)
	}
	if last["context"] != sessionClaimContext || last["target_id"] != sessionRunID {
		t.Fatalf("checkpoint internal request = %#v", last)
	}
}

func TestDevtoolsSessionCoordinationClearsOnlyAfterSuccessfulTerminalMutation(t *testing.T) {
	failRelease := true
	runtime := runtimeFixture(func(_ context.Context, request any) (json.RawMessage, error) {
		row := request.(map[string]any)
		if row["action"] == string(devtools.CoordinationRelease) && failRelease {
			return nil, errors.New("synthetic release failure")
		}
		public := map[string]any{"profile": "fixture", "revision": 3, "request_id": row["request_id"], "changed": true}
		response := map[string]any{"public": public, "profile": "fixture"}
		if row["action"] == string(devtools.CoordinationClaim) {
			public["claimed"] = true
			response["context"] = sessionClaimContext
			response["context_valid"] = true
			response["run_id"] = sessionRunID
			response["task_id"] = sessionTaskID
		} else if row["action"] == string(devtools.CoordinationCheckpoint) {
			response["context_valid"] = true
		} else {
			response["context_valid"] = false
		}
		raw, _ := json.Marshal(response)
		return raw, nil
	})
	controller := &DevtoolsSessionCoordination{Runtime: runtime, Claims: NewDevtoolsSessionClaims()}
	if _, err := controller.Mutate(t.Context(), "session-a", devtools.CoordinationClaim, DevtoolsSessionMutationRequest{
		TargetID: sessionTaskID, RequestID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Mutate(t.Context(), "session-a", devtools.CoordinationRelease, DevtoolsSessionMutationRequest{
		TargetID: sessionRunID, RequestID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
	}); err == nil {
		t.Fatal("synthetic release failure was ignored")
	}
	if _, ok := controller.Claims.get("session-a"); !ok {
		t.Fatal("failed release cleared session ownership")
	}

	failRelease = false
	if _, err := controller.Mutate(t.Context(), "session-a", devtools.CoordinationRelease, DevtoolsSessionMutationRequest{
		TargetID: sessionRunID, RequestID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := controller.Claims.get("session-a"); ok {
		t.Fatal("successful release retained session ownership")
	}
	if _, err := controller.Mutate(t.Context(), "session-a", devtools.CoordinationCheckpoint, DevtoolsSessionMutationRequest{
		TargetID: sessionRunID, RequestID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", Summary: "late",
	}); err == nil {
		t.Fatal("checkpoint succeeded after release")
	}
}

func TestDevtoolsSessionTakeoverReplacesBindingOnlyOnSuccess(t *testing.T) {
	fail := true
	runtime := runtimeFixture(func(_ context.Context, request any) (json.RawMessage, error) {
		row := request.(map[string]any)
		if row["action"] == string(devtools.CoordinationTakeover) && fail {
			return nil, errors.New("synthetic takeover failure")
		}
		public := map[string]any{"profile": "fixture", "revision": 4, "request_id": row["request_id"], "changed": true, "claimed": true}
		response := map[string]any{"public": public, "profile": "fixture", "context": "replacement-context", "context_valid": true, "run_id": sessionOtherRunID, "task_id": sessionTaskID}
		raw, _ := json.Marshal(response)
		return raw, nil
	})
	claims := NewDevtoolsSessionClaims()
	if err := claims.bind("session-a", devtoolsClaimBinding{Profile: "fixture", TaskID: sessionTaskID, RunID: sessionRunID, context: sessionClaimContext}); err != nil {
		t.Fatal(err)
	}
	controller := &DevtoolsSessionCoordination{Runtime: runtime, Claims: claims}
	request := DevtoolsSessionMutationRequest{
		TargetID: sessionTaskID, ExpectedRunID: sessionRunID, RequestID: "ffffffff-ffff-4fff-8fff-ffffffffffff",
	}
	if _, err := controller.Mutate(t.Context(), "session-a", devtools.CoordinationTakeover, request); err == nil {
		t.Fatal("synthetic takeover failure was ignored")
	}
	if binding, _ := claims.get("session-a"); binding.context != sessionClaimContext || binding.RunID != sessionRunID {
		t.Fatalf("failed takeover changed binding: %#v", binding)
	}
	fail = false
	if _, err := controller.Mutate(t.Context(), "session-a", devtools.CoordinationTakeover, request); err != nil {
		t.Fatal(err)
	}
	if binding, _ := claims.get("session-a"); binding.context != "replacement-context" || binding.RunID != sessionOtherRunID {
		t.Fatalf("successful takeover binding = %#v", binding)
	}
}

func TestDevtoolsSessionClaimBindingTransfersRunOwnership(t *testing.T) {
	claims := NewDevtoolsSessionClaims()
	first := devtoolsClaimBinding{Profile: "fixture", TaskID: sessionTaskID, RunID: sessionRunID, context: "first-context"}
	second := devtoolsClaimBinding{Profile: "fixture", TaskID: sessionTaskID, RunID: sessionRunID, context: "second-context"}
	if err := claims.bind("session-a", first); err != nil {
		t.Fatal(err)
	}
	if err := claims.bind("session-b", second); err != nil {
		t.Fatal(err)
	}
	if _, ok := claims.get("session-a"); ok {
		t.Fatal("previous session retained Run ownership after transfer")
	}
	if got, ok := claims.get("session-b"); !ok || got.context != "second-context" {
		t.Fatalf("transferred binding = %#v, %v", got, ok)
	}
	if claims.count() != 1 {
		t.Fatalf("claim count after transfer = %d", claims.count())
	}
}

func TestDevtoolsSessionClaimsAreBoundedAndMCPEntryRequiresSession(t *testing.T) {
	claims := NewDevtoolsSessionClaims()
	for i := 0; i < maxDevtoolsSessionClaims; i++ {
		session := fmt.Sprintf("session-%03d", i)
		if err := claims.bind(session, devtoolsClaimBinding{Profile: "fixture", TaskID: sessionTaskID, RunID: fmt.Sprintf("run-%03d", i), context: "private-" + session}); err != nil {
			t.Fatalf("bind %d: %v", i, err)
		}
	}
	if err := claims.bind("overflow", devtoolsClaimBinding{Profile: "fixture", TaskID: sessionTaskID, RunID: "overflow-run", context: "private-overflow"}); err == nil {
		t.Fatal("session claim capacity was not enforced")
	}
	controller := &DevtoolsSessionCoordination{Runtime: runtimeFixture(func(context.Context, any) (json.RawMessage, error) {
		t.Fatal("runtime called without MCP session binding")
		return nil, nil
	}), Claims: NewDevtoolsSessionClaims()}
	if _, err := controller.MutateMCP(context.Background(), devtools.CoordinationClaim, DevtoolsSessionMutationRequest{RequestID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}); err == nil || !strings.Contains(err.Error(), "session binding") {
		t.Fatalf("missing MCP session error = %v", err)
	}
}
