package mcptransport

import (
	"context"
	"net"
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
			capabilities := value["capabilities"].(map[string]any)
			integrations := value["integrations"].(map[string]any)
			if value["python_version"] != nil || value["go_version"] == "" || value["tool_catalog"].(map[string]any)["count"] != 35 || metadata.SHA256 != generation.Digest() ||
				capabilities["signed_git_commits"] != false ||
				integrations["browser"].(map[string]any)["state"] != integrationDisabled ||
				integrations["github"].(map[string]any)["state"] != integrationUnconfigured ||
				integrations["signing"].(map[string]any)["state"] != integrationUnconfigured {
				t.Fatal(value)
			}
		case "workspace":
			if value["root"] != "/workspace" || value["running_processes"] != nil {
				t.Fatal(value)
			}
		case "diagnostics":
			if value["healthy"] != true || value["core_healthy"] != true || len(value["repositories"].([]string)) != 2 {
				t.Fatal(value)
			}
			integrations := value["integrations"].(map[string]any)
			if integrations["browser"].(map[string]any)["state"] != integrationDisabled ||
				integrations["github"].(map[string]any)["state"] != integrationUnconfigured ||
				integrations["signing"].(map[string]any)["state"] != integrationUnconfigured {
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

func TestBrowserIntegrationStatusTracksSocketDynamically(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "browser.sock")
	paths := serviceFixture(t)
	cfg, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = paths.Root()
	controller := &SystemController{
		Config: cfg, Paths: paths, BrowserSocket: socket,
		GitEnvironment: []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"},
	}

	status := controller.integrationStatus(t.Context())
	browser := status["browser"].(map[string]any)
	if browser["state"] != integrationDisabled || browser["ready"] != false {
		t.Fatalf("browser status before enable=%#v", browser)
	}

	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	status = controller.integrationStatus(t.Context())
	browser = status["browser"].(map[string]any)
	if browser["state"] != integrationReady || browser["enabled"] != true || browser["ready"] != true {
		t.Fatalf("browser status after enable=%#v", browser)
	}
}
