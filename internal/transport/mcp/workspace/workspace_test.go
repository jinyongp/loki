package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"loki/internal/config"
	workworkspace "loki/internal/work/workspace"
)

func TestWorkspaceEditBatchServiceDecodesStructuredOperations(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	files, err := workworkspace.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	handler := WorkspaceHandlers(files)["workspace_edit"]
	result, err := handler(t.Context(), map[string]any{
		"action":     "batch",
		"request_id": "60000000-0000-4000-8000-000000000001",
		"operations": []any{
			map[string]any{
				"action": "create", "path": "created.txt",
				"content": "created\n", "expected_sha256": "missing",
			},
		},
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("batch result = %#v, err = %v", result, err)
	}
	value := result.StructuredContent.(map[string]any)
	if value["state"] != "applied" || value["request_id"] != "60000000-0000-4000-8000-000000000001" {
		t.Fatalf("batch value = %#v", value)
	}
	data, err := os.ReadFile(filepath.Join(c.Root, "created.txt"))
	if err != nil || string(data) != "created\n" {
		t.Fatalf("created file = %q, %v", data, err)
	}
}

func TestWorkspaceEditMoveConsumesCASPreconditions(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	files, err := workworkspace.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	if _, err = files.Create("source.txt", "value\n"); err != nil {
		t.Fatal(err)
	}
	read, err := files.Read("source.txt", 0, 10)
	if err != nil {
		t.Fatal(err)
	}

	handler := WorkspaceHandlers(files)["workspace_edit"]
	result, err := handler(t.Context(), map[string]any{
		"action": "move", "source": "source.txt", "destination": "moved.txt",
		"expected_sha256": read["sha256"], "expected_destination": "missing",
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("move result = %#v, err = %v", result, err)
	}
	if _, err = os.Stat(filepath.Join(c.Root, "source.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(c.Root, "moved.txt"))
	if err != nil || string(data) != "value\n" {
		t.Fatalf("moved file = %q, %v", data, err)
	}
}
