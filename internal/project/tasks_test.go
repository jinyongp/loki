package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loki/internal/process"
)

const testUUID = "11111111-1111-4111-8111-111111111111"

func TestTaskInputBoundaries(t *testing.T) {
	for _, value := range []any{"1", "rc.hooks=on", "../task", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", nil} {
		if _, err := TaskUUID(value); err == nil {
			t.Fatalf("UUID accepted %v", value)
		}
	}
	if _, err := TaskUUID(testUUID); err != nil {
		t.Fatal(err)
	}
	for _, fields := range []map[string]any{{"project": "other"}, {"priority": []any{}}, {"tags": []any{"rc.hooks=on"}}, {"depends": []any{"1"}}, {"due": "tomorrow"}, {"description": ""}, {"description": "nul\x00"}, {"tags": []any{nil}}, {"depends": []any{false}}} {
		if _, err := TaskAttributes(fields); err == nil {
			t.Fatalf("fields accepted %v", fields)
		}
	}
}
func TestTaskNextAndScope(t *testing.T) {
	rows := []map[string]any{{"uuid": testUUID, "status": "pending", "description": "ready", "urgency": 1}, {"uuid": "22222222-2222-4222-8222-222222222222", "status": "pending", "depends": []string{testUUID}, "urgency": 100}, {"uuid": "33333333-3333-4333-8333-333333333333", "status": "pending", "scheduled": "29990101T000000Z"}, {"uuid": "44444444-4444-4444-8444-444444444444", "status": "pending", "urgency": 2}}
	encoded, _ := json.Marshal(rows)
	run := func(context.Context, []string) (process.Result, error) {
		return process.Result{Output: string(encoded)}, nil
	}
	out, err := OperateTask(t.Context(), run, TaskRequest{Action: "next"}, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ready := out["tasks"].([]map[string]any)
	if len(ready) != 2 || ready[0]["uuid"] != rows[3]["uuid"] || ready[1]["uuid"] != testUUID {
		t.Fatalf("next %v", out)
	}
	for _, r := range []TaskRequest{{Action: "list", Limit: ptr(0)}, {Action: "count", Limit: ptr(201)}, {Action: "list", Offset: -1}, {Action: "list", Status: "unknown"}} {
		if _, err = OperateTask(t.Context(), run, r, "test", time.Now()); err == nil {
			t.Fatalf("invalid request accepted %+v", r)
		}
	}
	var calls [][]string
	missing := func(_ context.Context, args []string) (process.Result, error) {
		calls = append(calls, args)
		return process.Result{Output: "[]"}, nil
	}
	_, err = OperateTask(t.Context(), missing, TaskRequest{Action: "delete", UUID: ptr(testUUID)}, "other", time.Now())
	if err == nil || !strings.Contains(err.Error(), "TASK_NOT_FOUND") || len(calls) != 1 || calls[0][len(calls[0])-1] != "export" {
		t.Fatalf("wrong-scope mutation %v %v", calls, err)
	}
}
func TestRealTaskLifecycleAndWorktreeSharing(t *testing.T) {
	binary := "/home/linuxbrew/.linuxbrew/bin/task"
	if _, err := os.Stat(binary); err != nil {
		t.Skip("Taskwarrior binary unavailable")
	}
	s, repo, secondary := fixture(t)
	tasks := Tasks{Store: s, Binary: binary, Home: t.TempDir()}
	status, err := tasks.Do(t.Context(), TaskRequest{CWD: repo, Action: "status"})
	if err != nil || status["available"] != true {
		t.Fatalf("task version preflight %v %v", status, err)
	}
	if _, err = tasks.Do(t.Context(), TaskRequest{CWD: repo, Action: "list"}); err == nil || !strings.Contains(err.Error(), "TASK_NOT_INITIALIZED") {
		t.Fatalf("uninitialized task %v", err)
	}
	slug := initialize(t, s, repo)
	id, err := s.Resolve(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := tasks.run(t.Context(), id, []string{"diagnostics"}, 10*time.Second)
	if err != nil || diagnostics.ExitCode != 0 || !strings.Contains(diagnostics.Output, "task "+status["version"].(string)) {
		t.Fatalf("isolated Taskwarrior diagnostics %s %v", diagnostics.Output, err)
	}
	if !strings.Contains(diagnostics.Output, filepath.Join(id.StateDirectory, "taskwarrior")) {
		t.Fatal("diagnostics did not confirm isolated task store")
	}
	status, err = tasks.Do(t.Context(), TaskRequest{CWD: repo, Action: "diagnostics"})
	if err != nil || status["health"].(map[string]any)["runner_access"] != true {
		t.Fatalf("health %v %v", status, err)
	}
	op := func(r TaskRequest) map[string]any {
		t.Helper()
		if r.CWD == "" {
			r.CWD = repo
		}
		out, err := tasks.Do(t.Context(), r)
		if err != nil {
			t.Fatalf("%s: %v", r.Action, err)
		}
		return out
	}
	first := op(TaskRequest{Action: "add", Fields: map[string]any{"description": "rc.hooks=on harmless literal", "tags": []string{"validation"}}})["task"].(map[string]any)
	uuid := first["uuid"].(string)
	if first["description"] != "rc.hooks=on harmless literal" || first["project"] != slug {
		t.Fatalf("unsafe task attributes %v", first)
	}
	note := op(TaskRequest{Action: "annotate", UUID: &uuid, Annotation: ptr("rc.hooks=on literal annotation")})["task"].(map[string]any)
	annotations := note["annotations"].([]any)
	if annotations[len(annotations)-1].(map[string]any)["description"] != "rc.hooks=on literal annotation" {
		t.Fatalf("annotation %v", note)
	}
	second := op(TaskRequest{Action: "add", Fields: map[string]any{"description": "blocked", "depends": []string{uuid}}})["task"].(map[string]any)
	secondID := second["uuid"].(string)
	if _, err = tasks.Do(t.Context(), TaskRequest{CWD: repo, Action: "modify", UUID: &uuid, Fields: map[string]any{"depends": []string{secondID}}}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle %v", err)
	}
	page := op(TaskRequest{Action: "list", Limit: ptr(1)})
	if page["count"] != 2 || page["has_more"] != true {
		t.Fatalf("page %v", page)
	}
	if op(TaskRequest{Action: "next"})["count"] != 1 {
		t.Fatal("blocked task is ready")
	}
	if _, err = tasks.Do(t.Context(), TaskRequest{CWD: secondary, Action: "list"}); err == nil || !strings.Contains(err.Error(), "TASK_WORKSTREAM_REQUIRED") {
		t.Fatalf("secondary binding %v", err)
	}
	if _, err = s.Bind(t.Context(), secondary, slug); err != nil {
		t.Fatal(err)
	}
	if op(TaskRequest{CWD: secondary, Action: "get", UUID: &uuid})["task"].(map[string]any)["uuid"] != uuid {
		t.Fatal("secondary queue differs")
	}
	if op(TaskRequest{Action: "start", UUID: &uuid})["task"].(map[string]any)["start"] == nil {
		t.Fatal("start absent")
	}
	if _, present := op(TaskRequest{Action: "stop", UUID: &uuid})["task"].(map[string]any)["start"]; present {
		t.Fatal("stop failed")
	}
	if op(TaskRequest{Action: "modify", UUID: &uuid, Fields: map[string]any{"priority": "H"}})["task"].(map[string]any)["priority"] != "H" {
		t.Fatal("priority update failed")
	}
	if op(TaskRequest{Action: "done", UUID: &uuid})["task"].(map[string]any)["status"] != "completed" {
		t.Fatal("done failed")
	}
	if op(TaskRequest{Action: "next"})["count"] != 1 {
		t.Fatal("completed dependency still blocks")
	}
	if op(TaskRequest{Action: "delete", UUID: &secondID})["task"].(map[string]any)["status"] != "deleted" {
		t.Fatal("logical delete failed")
	}
	if op(TaskRequest{Action: "count", Status: "all"})["count"] != 2 {
		t.Fatal("logical deletion purged data")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			clone := tasks
			store, err := New(s.WorkspaceRoot, s.StateRoot)
			if err != nil {
				t.Error(err)
				return
			}
			clone.Store = store
			_, err = clone.Do(t.Context(), TaskRequest{CWD: secondary, Action: "add", Fields: map[string]any{"description": "concurrent addition"}})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if op(TaskRequest{Action: "count", Status: "all"})["count"] != 10 {
		t.Fatal("concurrent additions lost")
	}
}
