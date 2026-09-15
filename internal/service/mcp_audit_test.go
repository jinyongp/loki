package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/audit"
	"loki/internal/mcpserver"
)

func TestMCPAuditRecordsOnlySelectedMetadata(t *testing.T) {
	log := &audit.Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	for _, mode := range []string{"success", "exit", "error", "panic"} {
		handler := auditHandler(log, "browser_interact", func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
			switch mode {
			case "error":
				return nil, errors.New("private error text")
			case "panic":
				panic("private panic text")
			case "exit":
				return mcpserver.Object(map[string]any{"exit_code": 1, "output": "private output"})
			}
			return mcpserver.Object(map[string]any{"output": "private output", "body": "private body"})
		}, func(err error) { t.Error(err) })
		func() {
			defer func() {
				if mode == "panic" && recover() == nil {
					t.Error("panic was swallowed")
				}
			}()
			_, _ = handler(t.Context(), map[string]any{"action": "type", "index": 1, "text": "private typed text", "content": "private file", "arguments": []string{"private command"}, "value": "private secret", "url": "https://example.test/?token=private", "path": strings.Repeat("private long path", 1000)})
		}()
	}
	data, err := os.ReadFile(log.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private") {
		t.Fatal("private request/result data leaked into audit")
	}
	result, err := log.Read(10)
	if err != nil {
		t.Fatal(err)
	}
	rows := result["records"].([]json.RawMessage)
	if len(rows) != 4 {
		t.Fatal(len(rows))
	}
	for i, row := range rows {
		var record map[string]any
		if err = json.Unmarshal(row, &record); err != nil {
			t.Fatal(err)
		}
		if record["success"] != (i == 3) {
			t.Fatal(record)
		}
		if record["tool"] != "browser_interact" || record["metadata"].(map[string]any)["action"] != "type" {
			t.Fatal(record)
		}
	}
}

func TestGitHubMCPAuditOmitsCommandContent(t *testing.T) {
	log := &audit.Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	handler := auditHandler(log, "github", func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
		return mcpserver.Object(map[string]any{"exit_code": 0, "output": "private-output-sentinel"})
	}, func(err error) { t.Error(err) })
	result, err := handler(t.Context(), map[string]any{
		"target": "owner/repo", "args": []string{"issue", "create", "--body", "private-argument-sentinel"},
		"input": "private-input-sentinel", "token": "private-token-sentinel",
	})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	data, err := os.ReadFile(log.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, private := range []string{"private-output-sentinel", "private-argument-sentinel", "private-input-sentinel", "private-token-sentinel"} {
		if strings.Contains(text, private) {
			t.Fatalf("audit leaked %q: %s", private, text)
		}
	}
	if !strings.Contains(text, "\"target\":\"owner/repo\"") || !strings.Contains(text, "\"exit_code\":0") {
		t.Fatal("safe GitHub audit metadata missing", text)
	}
}
