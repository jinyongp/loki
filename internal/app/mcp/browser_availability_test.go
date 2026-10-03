package mcpapp

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/config"
	"loki/internal/portguard"
)

func TestBrowserDiscoveryTracksEnableDisableAndRejectsCachedCalls(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root, c.AuditLog = t.TempDir(), filepath.Join(t.TempDir(), "audit.jsonl")
	socket := filepath.Join(t.TempDir(), "browser.sock")
	runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	var calls atomic.Int32
	browser := browserFixture(func(context.Context, string, map[string]any) (map[string]any, error) {
		calls.Add(1)
		return map[string]any{"status": "running", "browser_generation": 1}, nil
	})
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewMCP(c, MCPOptions{
		Runtime: runtime, PortGuard: runtime, Browser: browser, BrowserSocket: socket,
		Jobs: jobControllerFixture(), GitJobs: gitJobsFixture(t, c, nil), JobToolchains: emptyJobToolchainResolver{},
		Ports: ports, Policy: policyGenerationFixture(t), Token: strings.Repeat("t", 43),
		Environment: map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := app.Server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "browser-selection-test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	assertCatalog := func(want int) {
		t.Helper()
		listed, err := client.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, tool := range listed.Tools {
			if strings.HasPrefix(tool.Name, "browser_") {
				count++
			}
		}
		if count != want {
			t.Fatalf("browser discovery count=%d want=%d", count, want)
		}
		for _, action := range []string{"server", "diagnostics"} {
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "system_inspect", Arguments: map[string]any{"action": action}})
			if err != nil || result.IsError {
				t.Fatalf("system %s: %v %v", action, result, err)
			}
			raw, _ := json.Marshal(result.StructuredContent)
			var info map[string]any
			if err := json.Unmarshal(raw, &info); err != nil {
				t.Fatal(err)
			}
			catalog := info["tool_catalog"].(map[string]any)
			if int(catalog["count"].(float64)) != len(listed.Tools) {
				t.Fatalf("system %s disagrees with tools/list: %#v", action, catalog)
			}
		}
	}
	assertBlocked := func() {
		t.Helper()
		for _, name := range []string{"browser_session", "browser_observe", "browser_interact", "browser_screenshot", "browser_save_screenshot", "browser_share_screenshot"} {
			// A cached client may send even incomplete arguments. The availability
			// guard must reject them before validation or side effects.
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
			if err != nil || !result.IsError || result.Meta["loki/error"].(map[string]any)["code"] != "unavailable" {
				t.Fatalf("cached %s call: %#v %v", name, result, err)
			}
		}
	}
	assertCatalog(0)
	assertBlocked()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	assertCatalog(6)
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "browser_session", Arguments: map[string]any{"action": "start"}})
	if err != nil || result.IsError || calls.Load() != 1 {
		t.Fatalf("enabled browser: %#v %v calls=%d", result, err, calls.Load())
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	assertCatalog(0)
	assertBlocked()
	if calls.Load() != 1 {
		t.Fatalf("disabled browser received calls: %d", calls.Load())
	}
}
