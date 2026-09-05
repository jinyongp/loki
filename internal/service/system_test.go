package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"loki/internal/config"
	"loki/internal/process"
)

func TestSystemInformation(t *testing.T) {
	_, paths := serviceFixture(t)
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = paths.Root()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	manager, err := process.NewManager(process.ManagerOptions{MaxProcesses: 1, MaxOutputBytes: 4096, Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	system := &SystemController{Config: c, Paths: paths, Processes: manager, Started: time.Now(), Artifacts: true, Previews: true, GitEnvironment: []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, InspectPort: func(ctx context.Context, port int) (map[string]any, error) {
		return map[string]any{"port": port, "in_use": false, "listeners": []any{}}, nil
	}}
	h := SystemHandler(system)
	for _, action := range []string{"server", "workspace", "diagnostics", "port"} {
		r, err := h(t.Context(), map[string]any{"action": action, "port": 43000})
		if err != nil {
			t.Fatal(action, err)
		}
		value := r.StructuredContent.(map[string]any)
		switch action {
		case "server":
			if value["python_version"] != nil || value["go_version"] == "" || value["tool_catalog"].(map[string]any)["count"] != 37 {
				t.Fatal(value)
			}
		case "workspace":
			if value["root"] != "/workspace" || value["running_processes"] != 0 {
				t.Fatal(value)
			}
		case "diagnostics":
			if value["healthy"] != false || len(value["repositories"].([]string)) != 2 {
				t.Fatal(value)
			}
		case "port":
			if value["port"] != 43000 {
				t.Fatal(value)
			}
		}
	}
}
