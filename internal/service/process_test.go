package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/config"
	"loki/internal/contract"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/process"
)

func TestProcessInspectMCPConfiguredSession(t *testing.T) {
	root := t.TempDir()
	paths, err := policy.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()
	manager, err := process.NewManager(process.ManagerOptions{MaxProcesses: 2, MaxOutputBytes: 4096, Retention: time.Minute, StopGrace: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	t.Setenv("LOKI_TEST_SERVER_PRIVATE", "must-not-inherit")
	configuration, err := config.Parse([]byte(`[processes.fixture]
command = ["/bin/sh", "-c", "printf '%s/%s/%s/' \"$HOME\" \"$CUSTOM\" \"${LOKI_TEST_SERVER_PRIVATE:-unset}\"; pwd; printf err >&2; exit 7"]
environment = { CUSTOM = "fixture" }
timeout_seconds = 10
max_output_bytes = 4096
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.StartConfigured(configuration, paths, "unknown", false); err == nil || err.Error() != "unknown process: unknown" {
		t.Fatalf("unknown process: %v", err)
	}
	if _, err = manager.StartConfigured(configuration, paths, "unknown", true); err == nil || err.Error() != "unknown check: unknown" {
		t.Fatalf("unknown check: %v", err)
	}
	configuration.Checks["check"] = configuration.Processes["fixture"]
	if _, err = manager.StartConfigured(configuration, paths, "check", true); err != nil {
		t.Fatal(err)
	}
	started, err := manager.StartConfigured(configuration, paths, "fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	id := started["session_id"].(string)
	handlers := map[string]mcpserver.Handler{"process_inspect": ProcessInspectHandler(manager)}
	baseline, _ := contract.Baseline()
	definitions, _ := baseline.Definitions()
	for _, definition := range definitions {
		if handlers[definition.Name] == nil {
			handlers[definition.Name] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return nil, errors.New("outside process inspection test scope")
			}
		}
	}
	server, err := mcpserver.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "isolated-test", Version: "1"}, nil).Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "process_inspect", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	decode := func(result *mcp.CallToolResult) map[string]any {
		t.Helper()
		if result.IsError {
			t.Fatalf("MCP error: %v", result.Content)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err = json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	deadline := time.Now().Add(5 * time.Second)
	var completed map[string]any
	for {
		completed = decode(call(map[string]any{"action": "read", "session_id": id}))
		if completed["status"] == "exited" && strings.HasSuffix(completed["output"].(string), "err") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("configured process failed to complete")
		}
		time.Sleep(5 * time.Millisecond)
	}
	want := "/tmp/fixture/unset/" + root + "\nerr"
	if completed["output"] != want || completed["exit_code"] != float64(7) {
		t.Fatalf("configured output: %#v", completed)
	}
	page := decode(call(map[string]any{"action": "read", "session_id": id, "offset": 0, "limit": 3}))
	if page["output"] != "/tm" || page["next_offset"] != float64(3) || page["has_more"] != true {
		t.Fatalf("page: %#v", page)
	}
	listed := decode(call(map[string]any{}))
	if len(listed["processes"].([]any)) != 2 {
		t.Fatalf("list: %#v", listed)
	}
	for _, args := range []map[string]any{{"action": "read"}, {"action": "read", "session_id": "missing"}, {"action": "invalid"}} {
		if !call(args).IsError {
			t.Fatalf("invalid read succeeded: %v", args)
		}
	}
}
