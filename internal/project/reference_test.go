package project

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"loki/internal/process"
)

func TestPython0471Differential(t *testing.T) {
	python := os.Getenv("LOKI_REFERENCE_PYTHON")
	if python == "" {
		python = "../../.tmp/python-baseline/bin/python"
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(python); err != nil {
		t.Skip("isolated Python reference environment unavailable")
	}
	s, _, _ := fixture(t)
	pythonState, err := os.MkdirTemp(t.TempDir(), "python-state-")
	if err != nil {
		t.Fatal(err)
	}
	init := map[string]any{"action": "init", "goal": " Ｓtraße\u001c 테스트\n", "slug": "shared-test-workstream", "slug_base": nil, "depth": "standard", "intent_source_kind": "plan-local", "intent_source": nil}
	cases := []map[string]any{
		{"action": "status"}, {"action": "list", "cwd": "feature"}, init, init,
		{"action": "status", "cwd": "feature"},
		{"action": "read", "cwd": "feature", "filename": "plan.md"},
		{"action": "bind", "cwd": "feature", "slug": "shared-test-workstream"},
		{"action": "status", "cwd": "feature"}, {"action": "list"},
		{"action": "read", "filename": "plan.md"},
		{"action": "write", "filename": "plan.md", "content": "initial\n"},
		{"action": "read", "cwd": "feature", "filename": "plan.md"},
		{"action": "write", "filename": "plan.md", "content": "second\n"},
		{"action": "write", "filename": "plan.md", "content": "second\n", "expected_sha256": hash([]byte("initial\n"))},
		{"action": "write", "filename": "plan.md", "content": "stale\n", "expected_sha256": hash([]byte("initial\n"))},
		{"action": "write", "filename": "spec.md", "content": "absent\n", "expected_sha256": hash([]byte("initial\n"))},
		{"action": "read", "filename": "manifest.json"},
		{"action": "bind", "slug": "unknown-valid-slug"},
		{"action": "bind", "slug": "../escape"},
		{"action": "write", "filename": "spec.md", "content": "CRLF\r\nCR\r"},
		{"action": "read", "filename": "spec.md"},
		{"action": "uuid", "uuid": testUUID}, {"action": "uuid", "uuid": "{11111111111141118111111111111111}"}, {"action": "uuid", "uuid": nil},
	}
	for _, fields := range []map[string]any{
		{"description": "rc.hooks=on harmless literal", "priority": "H", "due": "2027-01-01", "tags": []string{"validation"}, "depends": []string{testUUID}},
		{"priority": nil, "tags": []string{}, "depends": []string{}, "wait": nil},
		{"project": "escape"}, {"description": ""}, {"description": "\x00"}, {"priority": false}, {"due": "tomorrow"}, {"tags": []any{true}}, {"depends": []any{nil}},
	} {
		cases = append(cases, map[string]any{"action": "attributes", "fields": fields})
	}
	rows := []map[string]any{
		{"uuid": testUUID, "status": "pending", "description": "ready", "urgency": 1},
		{"uuid": "22222222-2222-4222-8222-222222222222", "status": "pending", "depends": []string{testUUID}, "urgency": 100},
		{"uuid": "33333333-3333-4333-8333-333333333333", "status": "pending", "scheduled": "20260905T000000Z"},
		{"uuid": "44444444-4444-4444-8444-444444444444", "status": "waiting", "wait": "20260905T000000Z"},
		{"uuid": "55555555-5555-4555-8555-555555555555", "status": "completed"},
	}
	for _, r := range []map[string]any{{"action": "list"}, {"action": "next"}, {"action": "list", "status": "all", "offset": 1, "limit": 2}, {"action": "list", "offset": 1000}, {"action": "count", "status": "all"}, {"action": "count", "limit": 0}, {"action": "next", "offset": -1}, {"action": "list", "status": "bad"}} {
		cases = append(cases, map[string]any{"action": "task", "rows": rows, "request": r})
	}
	input, _ := json.Marshal(cases)
	cmd := exec.CommandContext(t.Context(), python, "testdata/python_reference.py", s.WorkspaceRoot, pythonState)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Python reference: %v\n%s", err, stderr.String())
	}
	var expected []map[string]any
	if err = json.Unmarshal(output, &expected); err != nil {
		t.Fatalf("reference JSON: %v %s", err, output)
	}
	if len(expected) != len(cases) {
		t.Fatal("missing reference results")
	}
	for i, c := range cases {
		actual := map[string]any{}
		value, err := runReferenceCase(t, s, c)
		if err != nil {
			actual["error"] = err.Error()
		} else {
			actual["result"] = value
		}
		encoded, _ := json.Marshal(actual)
		var normalized map[string]any
		if err = json.Unmarshal(encoded, &normalized); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(normalized, expected[i]) {
			t.Errorf("case %d (%v)\nGo: %s\nPython: %v", i, c, encoded, expected[i])
		}
	}
	t.Logf("Compared %d project/task success and policy-error cases with Python 0.47.1", len(cases))
}

func runReferenceCase(t *testing.T, s *Store, c map[string]any) (any, error) {
	t.Helper()
	cwd := "project"
	if v, ok := c["cwd"].(string); ok {
		cwd = v
	}
	str := func(name string) string { v, _ := c[name].(string); return v }
	optional := func(name string) *string {
		v, ok := c[name].(string)
		if !ok {
			return nil
		}
		return &v
	}
	switch c["action"] {
	case "status":
		return s.Status(t.Context(), cwd)
	case "list":
		return s.List(t.Context(), cwd)
	case "init":
		data, _ := json.Marshal(c)
		var r InitRequest
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		return s.Initialize(t.Context(), cwd, r)
	case "bind":
		return s.Bind(t.Context(), cwd, str("slug"))
	case "read":
		return s.ReadArtifact(t.Context(), cwd, optional("slug"), str("filename"))
	case "write":
		return s.WriteArtifact(t.Context(), cwd, optional("slug"), str("filename"), str("content"), optional("expected_sha256"))
	case "uuid":
		return TaskUUID(c["uuid"])
	case "attributes":
		attrs, err := TaskAttributes(c["fields"].(map[string]any))
		sort.Strings(attrs)
		return attrs, err
	case "task":
		data, _ := json.Marshal(c["request"])
		var r TaskRequest
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		rows, _ := json.Marshal(c["rows"])
		return OperateTask(t.Context(), func(context.Context, []string) (process.Result, error) {
			return process.Result{Output: string(rows)}, nil
		}, r, "test-workstream-state", time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	default:
		t.Fatalf("unknown case %v", c)
	}
	return nil, nil
}
