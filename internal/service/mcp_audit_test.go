package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/audit"
	"loki/internal/fault"
	"loki/internal/mcpserver"
)

func readAuditLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	result := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		result = append(result, record)
	}
	return result
}

func TestMCPAuditRecordsCorrelatedStartAndTerminalMetadata(t *testing.T) {
	log := &audit.Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	modes := []string{"success", "exit", "error", "panic"}
	for _, mode := range modes {
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
			_, _ = handler(t.Context(), map[string]any{
				"action": "type", "index": 1, "text": "private typed text", "content": "private file",
				"arguments": []string{"private command"}, "value": "private secret",
				"url": "https://example.test/?token=private", "path": strings.Repeat("private long path", 1000),
			})
		}()
	}
	data, err := os.ReadFile(log.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private") {
		t.Fatal("private request/result data leaked into audit")
	}
	rows := readAuditLines(t, log.Path)
	if len(rows) != len(modes)*2 {
		t.Fatalf("audit rows = %d", len(rows))
	}
	expectedOutcome := map[string]string{
		"success": "success",
		"exit":    "nonzero_exit",
		"error":   "handler_error",
		"panic":   "panic",
	}
	for index, mode := range modes {
		start := rows[index*2]
		terminal := rows[index*2+1]
		if start["phase"] != "start" || terminal["phase"] != "terminal" {
			t.Fatalf("%s phases = %#v / %#v", mode, start, terminal)
		}
		if start["invocation_id"] == "" || start["invocation_id"] != terminal["invocation_id"] {
			t.Fatalf("%s invocation correlation = %#v / %#v", mode, start, terminal)
		}
		if start["tool"] != "browser_interact" || terminal["tool"] != "browser_interact" {
			t.Fatalf("%s tool = %#v / %#v", mode, start, terminal)
		}
		if start["metadata"].(map[string]any)["action"] != "type" || terminal["metadata"].(map[string]any)["action"] != "type" {
			t.Fatalf("%s metadata = %#v / %#v", mode, start, terminal)
		}
		if terminal["outcome"] != expectedOutcome[mode] {
			t.Fatalf("%s outcome = %v", mode, terminal["outcome"])
		}
		if terminal["success"] != (mode == "success") {
			t.Fatalf("%s success = %v", mode, terminal["success"])
		}
		if terminal["duration_ms"].(float64) < 0 {
			t.Fatalf("%s duration = %v", mode, terminal["duration_ms"])
		}
	}
}

func TestJobMCPAuditKeepsOnlySafeLifecycleMetadata(t *testing.T) {
	log := &audit.Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	handler := auditHandler(log, "job", func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
		return mcpserver.Object(map[string]any{
			"action": "start", "job_id": strings.Repeat("a", 32),
			"output": "private-job-output", "backend_ref": "private-backend",
		})
	}, func(err error) { t.Error(err) })
	_, err := handler(t.Context(), map[string]any{
		"action": "start", "request_id": "123e4567-e89b-12d3-a456-426614174301",
		"cwd": "repo", "argv": []string{"/bin/sh", "-c", "private-job-command"},
		"timeout_seconds": 30, "job_id": strings.Repeat("a", 32),
		"policy_sha256": "private-policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, private := range []string{"private-job-output", "private-backend", "private-job-command", "private-policy"} {
		if strings.Contains(text, private) {
			t.Fatalf("job audit leaked %q: %s", private, text)
		}
	}
	for _, safe := range []string{
		`"action":"start"`,
		`"request_id":"123e4567-e89b-12d3-a456-426614174301"`,
		`"job_id":"` + strings.Repeat("a", 32) + `"`,
		`"timeout_seconds":30`,
	} {
		if !strings.Contains(text, safe) {
			t.Fatalf("job audit missing safe metadata %s: %s", safe, text)
		}
	}
}

func TestJobInspectNonzeroExitRemainsAuditSuccess(t *testing.T) {
	log := &audit.Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	handler := auditHandler(log, "job", func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
		return mcpserver.Object(map[string]any{
			"action": "inspect", "job_id": strings.Repeat("a", 32),
			"state": "terminal", "exit_code": 7, "outcome": "exited",
		})
	}, func(err error) { t.Error(err) })
	result, err := handler(t.Context(), map[string]any{
		"action": "inspect", "job_id": strings.Repeat("a", 32),
	})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	rows := readAuditLines(t, log.Path)
	if len(rows) != 2 {
		t.Fatalf("audit rows = %#v", rows)
	}
	terminal := rows[1]
	if terminal["success"] != true || terminal["outcome"] != "success" {
		t.Fatalf("inspect audit terminal = %#v", terminal)
	}
	metadata := terminal["metadata"].(map[string]any)
	if metadata["exit_code"] != float64(7) || metadata["job_id"] != strings.Repeat("a", 32) {
		t.Fatalf("inspect audit metadata = %#v", metadata)
	}
}

func TestMCPAuditStartIsDurableBeforeHandlerCompletes(t *testing.T) {
	log := &audit.Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	handler := auditHandler(log, "git_stage", func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
		close(entered)
		<-release
		return mcpserver.Object(map[string]any{"ok": true})
	}, func(err error) { t.Error(err) })

	go func() {
		defer close(done)
		_, _ = handler(t.Context(), map[string]any{"operation": "commit-ready", "path": "repo"})
	}()
	<-entered

	rows := readAuditLines(t, log.Path)
	if len(rows) != 1 || rows[0]["phase"] != "start" || rows[0]["invocation_id"] == "" {
		t.Fatalf("in-flight audit = %#v", rows)
	}
	invocationID := rows[0]["invocation_id"]

	close(release)
	<-done
	rows = readAuditLines(t, log.Path)
	if len(rows) != 2 || rows[1]["phase"] != "terminal" || rows[1]["invocation_id"] != invocationID {
		t.Fatalf("terminal audit = %#v", rows)
	}
}

func TestMCPAuditPublishesCorrelationOnResultsAndErrors(t *testing.T) {
	log := &audit.Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	success := auditHandler(log, "workspace_edit", func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
		return mcpserver.Object(map[string]any{"ok": true})
	}, func(err error) { t.Error(err) })
	result, err := success(t.Context(), map[string]any{"action": "create", "path": "fixture.txt"})
	if err != nil || result == nil {
		t.Fatal(result, err)
	}
	correlationID, _ := result.Meta["loki/correlation_id"].(string)
	if correlationID == "" {
		t.Fatalf("result metadata = %#v", result.Meta)
	}

	failed := auditHandler(log, "workspace_edit", func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
		return nil, fault.New(fault.CodeConflict, "workspace changed", true, "read the current file revision")
	}, func(err error) { t.Error(err) })
	_, err = failed(t.Context(), map[string]any{"action": "replace", "path": "fixture.txt"})
	if err == nil {
		t.Fatal("expected typed error")
	}
	detail := fault.Describe(err)
	if detail.Code != fault.CodeConflict || detail.CorrelationID == "" || detail.CorrelationID == correlationID {
		t.Fatalf("error detail = %#v", detail)
	}
	if detail.NextAction != "read the current file revision" {
		t.Fatalf("error next action = %#v", detail)
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
	rows := readAuditLines(t, log.Path)
	if len(rows) != 2 || rows[0]["phase"] != "start" || rows[1]["phase"] != "terminal" {
		t.Fatalf("rows = %#v", rows)
	}
	if !strings.Contains(text, "\"target\":\"owner/repo\"") || !strings.Contains(text, "\"exit_code\":0") {
		t.Fatal("safe GitHub audit metadata missing", text)
	}
}
