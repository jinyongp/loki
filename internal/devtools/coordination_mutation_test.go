package devtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureClaimContext = "private-claim-context-canary"

func setMutationResponse(t *testing.T, client *Client, files map[string]string, data map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"schema_version": 1, "ok": true, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	response := filepath.Join(client.CWD, "mutation.json")
	if err := os.WriteFile(response, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.17.0\",\"commit\":\"test\",\"protocol_version\":3}}'; exit 0; fi\n" +
		"if [ \"$1 $2\" = \"schema --all\" ]; then cat \"" + files["catalog"] + "\"; exit 0; fi\n" +
		"if [ \"$1 $2\" = \"project inspect\" ]; then cat \"" + files["project"] + "\"; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > \"" + files["log"] + "\"\n" +
		"if [ \"$1\" = task ]; then cat \"" + response + "\"; exit 0; fi\n" +
		"exit 2\n"
	if err := os.WriteFile(client.Binary, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.verified = false
	client.mu.Unlock()
}

func mutationBase(client *Client) map[string]any {
	return map[string]any{
		"profile": "fixture", "revision": 2, "previous_revision": 1, "current_revision": 2,
		"affected_count": 1, "affected_ids": []string{fixtureTaskID}, "request_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"replayed": false, "changed": true, "action_ids": []string{"event"},
		"item": map[string]any{"id": fixtureTaskID, "kind": "task"},
		"run":  map[string]any{"id": fixtureRunID, "task_id": fixtureTaskID, "directory": client.CWD},
	}
}

func TestCoordinationClaimExtractsPrivateContextAndSanitizesPublicResult(t *testing.T) {
	client, files := metadataClient(t)
	data := mutationBase(client)
	data["claimed"] = true
	data["context"] = fixtureClaimContext
	data["context_valid"] = true
	setMutationResponse(t, client, files, data)

	result, err := client.MutateCoordination(t.Context(), ".", CoordinationClaim, CoordinationMutationRequest{
		RequestID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Target:    fixtureTaskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Context != fixtureClaimContext || !result.ContextValid || result.RunID != fixtureRunID || result.TaskID != fixtureTaskID || result.Profile != "fixture" {
		t.Fatalf("private result = %#v", result)
	}
	if strings.Contains(string(result.Public), fixtureClaimContext) || strings.Contains(string(result.Public), client.CWD) {
		t.Fatalf("public mutation leaked private data: %s", result.Public)
	}
	if !strings.Contains(string(result.Public), `"directory":"."`) {
		t.Fatalf("public mutation did not sanitize directory: %s", result.Public)
	}
	args, err := os.ReadFile(files["log"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"task", "claim", "--profile", "fixture", "--request-id", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "--dir", client.CWD, fixtureTaskID} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("claim args %q do not contain %q", args, want)
		}
	}
}

func TestCoordinationCheckpointRequiresAndUsesPrivateContext(t *testing.T) {
	client, files := metadataClient(t)
	data := mutationBase(client)
	data["run"] = nil
	data["context_valid"] = true
	delete(data, "context")
	setMutationResponse(t, client, files, data)

	if _, err := client.MutateCoordination(t.Context(), ".", CoordinationCheckpoint, CoordinationMutationRequest{
		RequestID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Target: fixtureRunID, Summary: "progress",
	}); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("missing context error = %v", err)
	}
	result, err := client.MutateCoordination(t.Context(), ".", CoordinationCheckpoint, CoordinationMutationRequest{
		RequestID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Target: fixtureRunID,
		Context: fixtureClaimContext, Summary: "progress", Decisions: []string{"keep typed boundary"}, Remaining: []string{"finish tests"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ContextValid || strings.Contains(string(result.Public), fixtureClaimContext) {
		t.Fatalf("checkpoint result = %#v", result)
	}
	args, err := os.ReadFile(files["log"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"task", "checkpoint", "--context", fixtureClaimContext, "--summary", "progress", fixtureRunID} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("checkpoint args %q do not contain %q", args, want)
		}
	}
}

func TestCoordinationMutationRejectsUnsafeInputsAndPrivateNestedOutput(t *testing.T) {
	client, files := metadataClient(t)
	if _, err := client.Call(t.Context(), "task claim", json.RawMessage([]byte("{}"))); err == nil || !strings.Contains(err.Error(), "typed adapter") {
		t.Fatalf("raw mutation call error = %v", err)
	}
	if _, err := client.MutateCoordination(t.Context(), "/tmp", CoordinationClaim, CoordinationMutationRequest{
		RequestID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}); err == nil {
		t.Fatal("outside claim directory accepted")
	}
	if _, err := client.MutateCoordination(t.Context(), ".", CoordinationClaim, CoordinationMutationRequest{
		RequestID: "not-a-uuid",
	}); err == nil {
		t.Fatal("invalid request ID accepted")
	}

	data := mutationBase(client)
	data["claimed"] = true
	data["context"] = fixtureClaimContext
	data["context_valid"] = true
	data["item"] = map[string]any{"id": fixtureTaskID, "kind": "task", "credential": "nested-private"}
	setMutationResponse(t, client, files, data)
	if _, err := client.MutateCoordination(t.Context(), ".", CoordinationClaim, CoordinationMutationRequest{
		RequestID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}); err == nil || !strings.Contains(err.Error(), "private context") {
		t.Fatalf("nested private field error = %v", err)
	}
}

func TestCoordinationCheckpointForwardsCompactionBasisOnlyForCheckpoint(t *testing.T) {
	client, files := metadataClient(t)
	data := mutationBase(client)
	data["run"] = nil
	data["context_valid"] = true
	delete(data, "context")
	setMutationResponse(t, client, files, data)

	fingerprint := strings.Repeat("c", 64)
	if _, err := client.MutateCoordination(t.Context(), ".", CoordinationCheckpoint, CoordinationMutationRequest{
		RequestID: "abababab-abab-4bab-8bab-abababababab", Target: fixtureRunID,
		Context: fixtureClaimContext, Summary: "compact",
		CompactionFingerprint: fingerprint, CompactionThrough: 12,
	}); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(files["log"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--compaction-fingerprint", fingerprint, "--compaction-through", "12"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("checkpoint args %q do not contain %q", args, want)
		}
	}

	if _, err := client.MutateCoordination(t.Context(), ".", CoordinationCheckpoint, CoordinationMutationRequest{
		RequestID: "cdcdcdcd-cdcd-4dcd-8dcd-cdcdcdcdcdcd", Target: fixtureRunID,
		Context: fixtureClaimContext, Summary: "bad", CompactionFingerprint: fingerprint,
	}); err == nil || !strings.Contains(err.Error(), "required together") {
		t.Fatalf("partial compaction basis error = %v", err)
	}
}
