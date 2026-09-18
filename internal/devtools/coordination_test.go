package devtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fixtureTaskID       = "11111111-1111-4111-8111-111111111111"
	fixtureRunID        = "22222222-2222-4222-8222-222222222222"
	fixtureWorkstreamID = "33333333-3333-4333-8333-333333333333"
)

func setCoordinationResponse(t *testing.T, client *Client, files map[string]string, data any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"schema_version": 1, "ok": true, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	response := filepath.Join(client.CWD, "coordination.json")
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

func TestCoordinationProjectionSanitizesDirectoriesAndPreservesContext(t *testing.T) {
	client, files := metadataClient(t)
	setCoordinationResponse(t, client, files, map[string]any{
		"profile":  "fixture",
		"revision": 12,
		"item": map[string]any{
			"id": fixtureTaskID, "kind": "task",
			"current_run": map[string]any{"id": fixtureRunID, "task_id": fixtureTaskID, "directory": client.CWD},
		},
		"documents":   map[string]any{"plan": map[string]any{"body": "keep this as untrusted project text"}},
		"tasks":       []any{},
		"validations": []any{},
		"history": []any{
			map[string]any{"id": "event-1", "action": "run.claimed", "data": map[string]any{"run_id": fixtureRunID, "directory": client.CWD}},
		},
		"truncated":   false,
		"omitted_ids": []string{},
	})
	got, err := client.QueryCoordination(t.Context(), ".", CoordinationTaskContext, CoordinationRequest{Target: fixtureTaskID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "fixture" || got.Revision != 12 || len(got.History) != 1 {
		t.Fatalf("projection = %#v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), client.CWD) || !strings.Contains(string(encoded), `"directory":"."`) {
		t.Fatalf("directory was not sanitized: %s", encoded)
	}
	if !strings.Contains(string(encoded), "untrusted project text") {
		t.Fatalf("context text was lost: %s", encoded)
	}
	args, err := os.ReadFile(files["log"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"task", "context", "--profile", "fixture", fixtureTaskID} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("task args %q do not contain %q", args, want)
		}
	}
}

func TestCoordinationRejectsPrivateContextAndOutsideDirectory(t *testing.T) {
	client, files := metadataClient(t)
	setCoordinationResponse(t, client, files, map[string]any{
		"profile": "fixture", "revision": 2,
		"item":      map[string]any{"id": fixtureTaskID},
		"documents": map[string]any{}, "tasks": []any{}, "validations": []any{},
		"history":   []any{map[string]any{"data": map[string]any{"context": "private-claim-context"}}},
		"truncated": false, "omitted_ids": []string{},
	})
	if _, err := client.QueryCoordination(t.Context(), ".", CoordinationTaskContext, CoordinationRequest{Target: fixtureTaskID}); err == nil || !strings.Contains(err.Error(), "private context") {
		t.Fatalf("private context error = %v", err)
	}

	client, files = metadataClient(t)
	setCoordinationResponse(t, client, files, map[string]any{
		"profile": "fixture", "revision": 3,
		"items":       []any{map[string]any{"id": fixtureRunID, "directory": "/tmp/outside"}},
		"next_cursor": nil,
	})
	if _, err := client.QueryCoordination(t.Context(), ".", CoordinationTaskCurrent, CoordinationRequest{}); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("outside directory error = %v", err)
	}
}

func TestCoordinationUsesTypedQueriesAndPagination(t *testing.T) {
	client, files := metadataClient(t)
	if _, err := client.Call(t.Context(), "task context", json.RawMessage([]byte("{}"))); err == nil || !strings.Contains(err.Error(), "typed adapter") {
		t.Fatalf("raw coordination call error = %v", err)
	}
	if _, err := client.QueryCoordination(t.Context(), ".", CoordinationQuery("task mutation"), CoordinationRequest{}); err == nil {
		t.Fatal("unsupported query accepted")
	}
	if _, err := client.QueryCoordination(t.Context(), ".", CoordinationTaskCurrent, CoordinationRequest{Limit: 201}); err == nil {
		t.Fatal("oversized query limit accepted")
	}

	next := "44444444-4444-4444-8444-444444444444"
	setCoordinationResponse(t, client, files, map[string]any{
		"profile": "fixture", "revision": 4,
		"items":       []any{map[string]any{"id": fixtureWorkstreamID, "kind": "workstream"}},
		"next_cursor": next,
	})
	got, err := client.QueryCoordination(t.Context(), ".", CoordinationWorkstreamList, CoordinationRequest{Limit: 25, Cursor: next})
	if err != nil {
		t.Fatal(err)
	}
	if got.NextCursor == nil || *got.NextCursor != next || len(got.Items) != 1 {
		t.Fatalf("paginated projection = %#v", got)
	}
	args, err := os.ReadFile(files["log"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"task", "workstream", "list", "--profile", "fixture", "--limit", "25", "--cursor", next} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("workstream args %q do not contain %q", args, want)
		}
	}
}

func TestCoordinationCurrentConfinesRequestedDirectory(t *testing.T) {
	client, files := metadataClient(t)
	if err := os.Mkdir(filepath.Join(client.CWD, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	setCoordinationResponse(t, client, files, map[string]any{
		"profile": "fixture", "revision": 5, "items": []any{}, "next_cursor": nil,
	})
	if _, err := client.QueryCoordination(t.Context(), "pkg", CoordinationTaskCurrent, CoordinationRequest{}); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(files["log"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), filepath.Join(client.CWD, "pkg")) {
		t.Fatalf("current query did not use confined directory: %q", args)
	}
	if _, err := client.QueryCoordination(t.Context(), "/tmp", CoordinationTaskCurrent, CoordinationRequest{}); err == nil {
		t.Fatal("outside current directory accepted")
	}
}

func TestCoordinationContextPreservesCompactionProjection(t *testing.T) {
	client, files := metadataClient(t)
	setCoordinationResponse(t, client, files, map[string]any{
		"profile": "fixture", "revision": 12,
		"item":      map[string]any{"id": fixtureTaskID, "kind": "task"},
		"documents": map[string]any{}, "tasks": []any{}, "validations": []any{}, "history": []any{},
		"truncated": false, "omitted_ids": []string{},
		"context_basis": map[string]any{"fingerprint": strings.Repeat("a", 64), "through_sequence": 10, "event_count": 4},
		"compaction":    map[string]any{"summary": "compact", "run_id": fixtureRunID, "through_sequence": 10},
		"delta":         []any{map[string]any{"action": "run.checkpointed", "data": map[string]any{"directory": client.CWD}}},
		"delta_status":  map[string]any{"truncated": false, "omitted_count": 0},
	})
	got, err := client.QueryCoordination(t.Context(), ".", CoordinationTaskContext, CoordinationRequest{Target: fixtureTaskID})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	for _, want := range []string{"context_basis", "compaction", "delta_status", "compact"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("projection missing %q: %s", want, encoded)
		}
	}
	if strings.Contains(string(encoded), client.CWD) || !strings.Contains(string(encoded), `"directory":"."`) {
		t.Fatalf("compaction projection path was not sanitized: %s", encoded)
	}

	client, files = metadataClient(t)
	setCoordinationResponse(t, client, files, map[string]any{
		"profile": "fixture", "revision": 13,
		"item":      map[string]any{"id": fixtureTaskID, "kind": "task"},
		"documents": map[string]any{}, "tasks": []any{}, "validations": []any{}, "history": []any{},
		"truncated": false, "omitted_ids": []string{},
		"context_basis": map[string]any{"fingerprint": strings.Repeat("b", 64), "through_sequence": 11},
		"compaction":    map[string]any{"summary": "unsafe", "credential": "private-canary"},
		"delta":         []any{}, "delta_status": map[string]any{"truncated": false},
	})
	if _, err := client.QueryCoordination(t.Context(), ".", CoordinationTaskContext, CoordinationRequest{Target: fixtureTaskID}); err == nil || !strings.Contains(err.Error(), "private context") {
		t.Fatalf("private compaction field error = %v", err)
	}
}
