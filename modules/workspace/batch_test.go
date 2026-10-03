package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/fault"
)

func batchFixtureOperationSet(t *testing.T, files *Files) []BatchOperation {
	t.Helper()
	if _, err := files.Create("replace.txt", "before\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Create("move.txt", "move\n"); err != nil {
		t.Fatal(err)
	}
	return []BatchOperation{
		{Action: "create", Path: "created.txt", Content: "created\n", ExpectedSHA256: "missing"},
		{
			Action: "replace", Path: "replace.txt", Old: "before", New: "after",
			ExpectedSHA256: Digest([]byte("before\n")), ExpectedReplacements: 1,
		},
		{
			Action: "move", Source: "move.txt", Destination: "moved.txt",
			ExpectedSHA256: Digest([]byte("move\n")), ExpectedDestination: "missing",
		},
	}
}

func requireWorkspaceContent(t *testing.T, files *Files, path, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(files.Policy.Root(), path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func TestBatchAppliesAndReplaysTerminalResult(t *testing.T) {
	files := fixture(t)
	operations := batchFixtureOperationSet(t, files)
	requestID := "10000000-0000-4000-8000-000000000001"

	first, err := files.Batch(t.Context(), requestID, operations)
	if err != nil {
		t.Fatal(err)
	}
	if first["request_id"] != requestID || first["state"] != batchStateApplied || first["operation_id"] == "" {
		t.Fatalf("batch result = %#v", first)
	}
	items, ok := first["files"].([]map[string]any)
	if !ok || len(items) != 3 {
		t.Fatalf("batch files = %#v", first["files"])
	}
	requireWorkspaceContent(t, files, "created.txt", "created\n")
	requireWorkspaceContent(t, files, "replace.txt", "after\n")
	requireWorkspaceContent(t, files, "moved.txt", "move\n")
	if _, err := os.Stat(filepath.Join(files.Policy.Root(), "move.txt")); !os.IsNotExist(err) {
		t.Fatalf("move source still exists: %v", err)
	}

	replay, err := files.Batch(t.Context(), requestID, operations)
	if err != nil {
		t.Fatal(err)
	}
	if replay["operation_id"] != first["operation_id"] || replay["state"] != batchStateApplied {
		t.Fatalf("replay = %#v, first = %#v", replay, first)
	}

	changed := append([]BatchOperation(nil), operations...)
	changed[0].Content = "different\n"
	if _, err := files.Batch(t.Context(), requestID, changed); err == nil {
		t.Fatal("reused request_id with different batch")
	} else if detail := fault.Describe(err); detail.Code != fault.CodeConflict {
		t.Fatalf("request identity error = %#v", detail)
	}
}

func TestBatchRejectsOverlappingPathsBeforeJournal(t *testing.T) {
	files := fixture(t)
	requestID := "15000000-0000-4000-8000-000000000001"
	operations := []BatchOperation{
		{Action: "create", Path: "parent", Content: "file\n", ExpectedSHA256: "missing"},
		{Action: "create", Path: "parent/child.txt", Content: "child\n", ExpectedSHA256: "missing"},
	}
	if _, err := files.Batch(t.Context(), requestID, operations); err == nil {
		t.Fatal("batch accepted parent/child path conflict")
	} else if detail := fault.Describe(err); detail.Code != fault.CodeInvalidInput {
		t.Fatalf("path conflict = %#v", detail)
	}
	path, err := files.batchRecordPath(requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path conflict published a journal: %v", err)
	}
}

func TestBatchPreflightFailureLeavesWorkspaceUntouched(t *testing.T) {
	files := fixture(t)
	if _, err := files.Create("existing.txt", "stable\n"); err != nil {
		t.Fatal(err)
	}
	requestID := "20000000-0000-4000-8000-000000000001"
	operations := []BatchOperation{
		{Action: "create", Path: "created.txt", Content: "created\n", ExpectedSHA256: "missing"},
		{
			Action: "replace", Path: "existing.txt", Old: "stable", New: "changed",
			ExpectedSHA256: strings.Repeat("0", 64), ExpectedReplacements: 1,
		},
	}

	if _, err := files.Batch(t.Context(), requestID, operations); err == nil {
		t.Fatal("batch accepted stale preflight")
	} else if detail := fault.Describe(err); detail.Code != fault.CodeConflict {
		t.Fatalf("preflight error = %#v", detail)
	}
	if _, err := os.Stat(filepath.Join(files.Policy.Root(), "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("preflight created a file: %v", err)
	}
	requireWorkspaceContent(t, files, "existing.txt", "stable\n")
	path, err := files.batchRecordPath(requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("preflight published a transaction record: %v", err)
	}
}

func TestBatchSynchronousFailureRollsBackAndReplaysFailure(t *testing.T) {
	files := fixture(t)
	if _, err := files.Create("existing.txt", "before\n"); err != nil {
		t.Fatal(err)
	}
	requestID := "30000000-0000-4000-8000-000000000001"
	operations := []BatchOperation{
		{Action: "create", Path: "created.txt", Content: "created\n", ExpectedSHA256: "missing"},
		{
			Action: "replace", Path: "existing.txt", Old: "before", New: "after",
			ExpectedSHA256: Digest([]byte("before\n")), ExpectedReplacements: 1,
		},
	}
	files.batchFault = func(stage string, index int) error {
		if stage == "before_apply" && index == 1 {
			return errors.New("synthetic apply failure")
		}
		return nil
	}

	_, err := files.Batch(t.Context(), requestID, operations)
	if err == nil {
		t.Fatal("batch failure was not reported")
	}
	detail := fault.Describe(err)
	if detail.Code != fault.CodeFailed || !strings.Contains(detail.Message, "rolled back") {
		t.Fatalf("batch failure = %#v", detail)
	}
	if _, statErr := os.Stat(filepath.Join(files.Policy.Root(), "created.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("rollback left created file: %v", statErr)
	}
	requireWorkspaceContent(t, files, "existing.txt", "before\n")

	_, replayErr := files.Batch(t.Context(), requestID, operations)
	if replayErr == nil {
		t.Fatal("rolled-back request replayed as success")
	}
	replayDetail := fault.Describe(replayErr)
	if replayDetail.Code != detail.Code || replayDetail.Message != detail.Message {
		t.Fatalf("replayed error = %#v, first = %#v", replayDetail, detail)
	}
}

func TestBatchCrashIsRecoveredBeforeNextMutation(t *testing.T) {
	files := fixture(t)
	if _, err := files.Create("existing.txt", "before\n"); err != nil {
		t.Fatal(err)
	}
	requestID := "40000000-0000-4000-8000-000000000001"
	operations := []BatchOperation{
		{Action: "create", Path: "created.txt", Content: "created\n", ExpectedSHA256: "missing"},
		{
			Action: "replace", Path: "existing.txt", Old: "before", New: "after",
			ExpectedSHA256: Digest([]byte("before\n")), ExpectedReplacements: 1,
		},
	}
	files.batchFault = func(stage string, index int) error {
		if stage == "after_apply" && index == 0 {
			panic("synthetic crash")
		}
		return nil
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("synthetic crash was not observed")
			}
		}()
		_, _ = files.Batch(t.Context(), requestID, operations)
	}()
	requireWorkspaceContent(t, files, "created.txt", "created\n")

	recovered, err := New(files.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	recovered.RGPath = files.RGPath
	if _, err := recovered.Create("trigger.txt", "ok\n"); err != nil {
		t.Fatalf("recovery gate blocked clean reconciliation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recovered.Policy.Root(), "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("crash recovery left created file: %v", err)
	}
	requireWorkspaceContent(t, recovered, "existing.txt", "before\n")
	requireWorkspaceContent(t, recovered, "trigger.txt", "ok\n")

	_, err = recovered.Batch(t.Context(), requestID, operations)
	if err == nil {
		t.Fatal("recovered request replayed as success")
	}
	detail := fault.Describe(err)
	if detail.Code != fault.CodeFailed || !strings.Contains(detail.Message, "rolled back during recovery") {
		t.Fatalf("recovered request error = %#v", detail)
	}
}

func TestBatchCrashRecoveryFailsClosedAfterExternalChange(t *testing.T) {
	files := fixture(t)
	requestID := "50000000-0000-4000-8000-000000000001"
	operations := []BatchOperation{
		{Action: "create", Path: "created.txt", Content: "created\n", ExpectedSHA256: "missing"},
	}
	files.batchFault = func(stage string, index int) error {
		if stage == "after_apply" && index == 0 {
			panic("synthetic crash")
		}
		return nil
	}
	func() {
		defer func() { _ = recover() }()
		_, _ = files.Batch(t.Context(), requestID, operations)
	}()
	if err := os.WriteFile(filepath.Join(files.Policy.Root(), "created.txt"), []byte("external\n"), 0600); err != nil {
		t.Fatal(err)
	}

	recovered, err := New(files.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, err := recovered.Create("trigger.txt", "blocked\n"); err == nil {
		t.Fatal("mutation continued after ambiguous crash recovery")
	} else if detail := fault.Describe(err); detail.Code != fault.CodeOutcomeUnknown {
		t.Fatalf("ambiguous recovery error = %#v", detail)
	}
	requireWorkspaceContent(t, recovered, "created.txt", "external\n")
	if _, err := os.Stat(filepath.Join(recovered.Policy.Root(), "trigger.txt")); !os.IsNotExist(err) {
		t.Fatalf("recovery failure allowed later mutation: %v", err)
	}
}
