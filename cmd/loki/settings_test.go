package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSettingsCLIValidationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("# fixture\n"), 0640)
	var restarts []string
	restart := func(unit string) error { restarts = append(restarts, unit); return nil }
	for _, test := range []struct {
		args  []string
		admin bool
		code  int
	}{
		{[]string{"config", "show"}, false, 0},
		{[]string{"config", "set", "max-action-processes", "16"}, false, 1},
		{[]string{"config", "set", "max-action-processes", "16"}, true, 0},
		{[]string{"config", "set", "max-action-processes", "2"}, true, 1},
		{[]string{"policy", "allow", "custom", "/usr/bin/true"}, true, 0},
		{[]string{"policy", "list"}, false, 0},
		{[]string{"policy", "deny", "missing"}, true, 1},
		{[]string{"policy", "deny", "custom"}, true, 0},
	} {
		var stdout, stderr bytes.Buffer
		code := executeSettings(t.Context(), test.args, path, test.admin, restart, &stdout, &stderr)
		if code != test.code {
			t.Fatalf("%q: %d %s", test.args, code, &stderr)
		}
	}
	if !reflect.DeepEqual(restarts, []string{"loki-runtime.service", "loki-mcp.service", "loki-mcp.service"}) {
		t.Fatalf("restarts %v", restarts)
	}
	var stdout, stderr bytes.Buffer
	code := executeSettings(t.Context(), []string{"config", "set", "max-action-processes", "20"}, path, true, func(string) error { return errors.New("unavailable") }, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "configuration saved; service restart failed") {
		t.Fatalf("restart failure %d %s", code, &stderr)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "20") || !strings.Contains(string(raw), "# fixture") {
		t.Fatalf("saved configuration %s", raw)
	}
}
