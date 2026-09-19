package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/audit"
	"loki/internal/mcpserver"
)

func appendActivityRecord(t *testing.T, log *audit.Log, record map[string]any) {
	t.Helper()
	if err := log.Append(record); err != nil {
		t.Fatal(err)
	}
}

func TestRecentToolActivityPairsTerminalAndUnmatchedStarts(t *testing.T) {
	log := &audit.Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	now := time.Date(2026, 9, 19, 0, 30, 10, 0, time.UTC)
	appendActivityRecord(t, log, map[string]any{
		"timestamp": "2026-09-19T00:30:00.000000+00:00", "phase": "start",
		"invocation_id": "a", "tool": "git_stage", "session_ref": "sess-a",
		"metadata": map[string]any{"operation": "paths"},
	})
	appendActivityRecord(t, log, map[string]any{
		"timestamp": "2026-09-19T00:30:00.250000+00:00", "phase": "terminal",
		"invocation_id": "a", "tool": "git_stage", "session_ref": "sess-a",
		"success": true, "outcome": "success", "duration_ms": 250.0,
		"metadata": map[string]any{"operation": "paths"},
	})
	appendActivityRecord(t, log, map[string]any{
		"timestamp": "2026-09-19T00:30:05.000000+00:00", "phase": "start",
		"invocation_id": "b", "tool": "git_commit", "session_ref": "sess-a",
		"metadata": map[string]any{"cwd": "repo"},
	})
	appendActivityRecord(t, log, map[string]any{
		"timestamp": "2026-09-19T00:30:09.000000+00:00", "phase": "start",
		"invocation_id": "self", "tool": "system_inspect", "session_ref": "sess-a",
		"metadata": map[string]any{"action": "activity"},
	})

	items, err := recentToolActivity(log, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].InvocationID != "b" || items[0].State != "started" || items[0].AgeSeconds == nil || *items[0].AgeSeconds != 5 {
		t.Fatalf("unmatched start = %#v", items[0])
	}
	if items[0].Success != nil || items[0].TerminalAt != "" {
		t.Fatalf("unmatched start has terminal fields = %#v", items[0])
	}
	if items[1].InvocationID != "a" || items[1].State != "terminal" || items[1].Success == nil || !*items[1].Success ||
		items[1].Outcome != "success" || items[1].DurationMS == nil || *items[1].DurationMS != 250 {
		t.Fatalf("terminal item = %#v", items[1])
	}
}

func TestRecentToolActivityContainsOnlyBoundedSafeMetadata(t *testing.T) {
	log := &audit.Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	handler := auditHandler(log, "github", func(_ context.Context, _ map[string]any) (*mcp.CallToolResult, error) {
		return mcpserver.Object(map[string]any{"exit_code": 0, "output": "private-output"})
	}, func(err error) { t.Error(err) })
	if _, err := handler(t.Context(), map[string]any{
		"target": "owner/repo", "args": []string{"private-argument"}, "input": "private-input",
	}); err != nil {
		t.Fatal(err)
	}
	items, err := recentToolActivity(log, 10, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), "private-") || !strings.Contains(string(raw), "owner/repo") {
		t.Fatalf("activity metadata = %s", raw)
	}
}
