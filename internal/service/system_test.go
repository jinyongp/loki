package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"loki/internal/audit"
	"loki/internal/config"
	controlpolicy "loki/internal/control/policy"
)

func TestSystemInformation(t *testing.T) {
	paths := serviceFixture(t)
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = paths.Root()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	generation := policyGenerationFixture(t)
	system := &SystemController{Config: c, Policy: generation, Paths: paths, Started: time.Now(), Artifacts: true, Previews: true, Audit: &audit.Log{Path: c.AuditLog}, GitEnvironment: []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, InspectPort: func(ctx context.Context, port int) (map[string]any, error) {
		return map[string]any{"port": port, "in_use": false, "listeners": []any{}}, nil
	}}
	h := SystemHandler(system)
	for _, action := range []string{"server", "workspace", "diagnostics", "activity", "port"} {
		r, err := h(t.Context(), map[string]any{"action": action, "port": 43000})
		if err != nil {
			t.Fatal(action, err)
		}
		value := r.StructuredContent.(map[string]any)
		switch action {
		case "server":
			metadata := value["policy_generation"].(controlpolicy.GenerationMetadata)
			if value["python_version"] != nil || value["go_version"] == "" || value["tool_catalog"].(map[string]any)["count"] != 29 || metadata.SHA256 != generation.Digest() {
				t.Fatal(value)
			}
		case "workspace":
			if value["root"] != "/workspace" || value["running_processes"] != nil {
				t.Fatal(value)
			}
		case "diagnostics":
			if value["healthy"] != false || len(value["repositories"].([]string)) != 2 {
				t.Fatal(value)
			}
		case "activity":
			if value["server_time"] == "" || len(value["items"].([]toolActivityItem)) != 0 {
				t.Fatal(value)
			}
		case "port":
			if value["port"] != 43000 {
				t.Fatal(value)
			}
		}
	}
}
