package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/config"
	"loki/internal/mcpserver"
	workworkspace "loki/internal/work/workspace"
)

func TestWorkspaceReadClampsRequestsThroughMCP(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	c.MaxReadLines, c.MaxListEntries, c.MaxSearchResults = 2, 2, 2
	files, err := workworkspace.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	files.RGPath = os.Getenv("LOKI_TEST_RG")
	if files.RGPath == "" {
		files.RGPath, err = exec.LookPath("rg")
		if err != nil {
			t.Fatal("ripgrep is required for workspace search validation")
		}
	}
	for _, name := range []string{"one.txt", "two.txt", "three.txt"} {
		if _, err := files.Create(name, strings.Repeat("needle\n", 5)); err != nil {
			t.Fatal(err)
		}
	}
	server, err := mcpserver.NewConfiguredAvailable(WorkspaceHandlers(files), mcpserver.ResourceOrigins{})
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	session, err := server.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "read-limits-test", Version: "1"}, nil).Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, args := range []map[string]any{
		{"action": "list", "limit": 1000000, "max_depth": 100},
		{"action": "file", "path": "one.txt", "limit": 1000000},
		{"action": "search", "query": "needle", "max_results": 1000000},
		{"action": "revisions", "path": "one.txt", "limit": 1000000},
	} {
		t.Run(args["action"].(string), func(t *testing.T) {
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "workspace_read", Arguments: args})
			if err != nil || result.IsError {
				t.Fatalf("large read request failed: %#v, %v", result, err)
			}
			value := result.StructuredContent.(map[string]any)
			switch args["action"] {
			case "list":
				if len(value["entries"].([]any)) != 2 || value["has_more"] != true {
					t.Fatalf("list was not bounded: %#v", value)
				}
			case "file":
				if value["content"] != "needle\nneedle\n" || value["eof"] != false {
					t.Fatalf("file was not bounded: %#v", value)
				}
			case "search":
				if len(value["matches"].([]any)) != 2 || value["truncated"] != true {
					t.Fatalf("search was not bounded: %#v", value)
				}
			}
		})
	}
	for _, args := range []map[string]any{
		{"action": "file", "path": "one.txt", "limit": -1},
		{"action": "file", "path": "one.txt", "limit": 1.5},
		{"action": "list", "limit": 10, "unexpected": true},
		{"action": "file", "path": "../outside.txt", "limit": 1000000},
	} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "workspace_read", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatalf("invalid read request accepted: args=%#v result=%#v err=%v", args, result, err)
		}
	}
}

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
