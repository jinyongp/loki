package mcpapp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/config"
	"loki/internal/portguard"
)

func TestProjectCoordinationMCPKeepsClaimContextSessionPrivate(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	c.Port, _ = strconv.Atoi(portText)
	token := strings.Repeat("c", 43)

	const privateContext = "mcp-private-claim-context-canary"
	const taskID = "11111111-1111-4111-8111-111111111111"
	const runID = "22222222-2222-4222-8222-222222222222"

	var mu sync.Mutex
	checkpointCalls := 0
	runtime := runtimeFixture(func(_ context.Context, request any) (json.RawMessage, error) {
		row := request.(map[string]any)
		switch row["operation"] {
		case "devtools_coordination_mutate":
			action, _ := row["action"].(string)
			public := map[string]any{"profile": "fixture", "revision": 2, "changed": true, "request_id": row["request_id"]}
			response := map[string]any{"public": public, "profile": "fixture"}
			switch action {
			case "task claim":
				public["claimed"] = true
				response["context"] = privateContext
				response["context_valid"] = true
				response["run_id"] = runID
				response["task_id"] = taskID
			case "task checkpoint":
				if row["context"] != privateContext || row["target_id"] != runID {
					t.Fatalf("checkpoint private request = %#v", row)
				}
				if row["compaction_fingerprint"] != nil || row["compaction_through"] != nil {
					t.Fatalf("checkpoint forwarded retired compaction fields: %#v", row)
				}
				mu.Lock()
				checkpointCalls++
				mu.Unlock()
				response["context_valid"] = true
			default:
				t.Fatalf("unexpected mutation action: %#v", row)
			}
			raw, _ := json.Marshal(response)
			return raw, nil
		case "devtools_task_context":
			raw, _ := json.Marshal(map[string]any{
				"profile": "fixture", "revision": 2,
				"item":      map[string]any{"id": taskID, "kind": "task", "running": true},
				"documents": map[string]any{}, "tasks": []any{}, "validations": []any{}, "history": []any{},
				"truncated": false, "omitted_ids": []string{},
			})
			return raw, nil
		default:
			return json.RawMessage(`{"initialized":true,"in_use":false,"listeners":[]}`), nil
		}
	})
	browser := browserFixture(func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"status": "running"}, nil
	})
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewMCP(c, MCPOptions{
		Runtime: runtime, PortGuard: runtime, Browser: browser, Jobs: jobControllerFixture(), GitJobs: gitJobsFixture(t, c, nil), JobToolchains: emptyJobToolchainResolver{}, Ports: ports,
		Policy: policyGenerationFixture(t), Token: token,
		Environment: map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server.Config.Handler = app
	server.Start()

	connect := func(name string) *mcp.ClientSession {
		client, err := mcp.NewClient(&mcp.Implementation{Name: name, Version: "1"}, nil).Connect(
			t.Context(),
			&mcp.StreamableClientTransport{
				Endpoint:             server.URL + "/mcp",
				HTTPClient:           &http.Client{Transport: bearerTransport{token}},
				DisableStandaloneSSE: true,
			},
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { client.Close() })
		return client
	}
	call := func(client *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
		if err != nil {
			t.Fatalf("%s transport: %v", name, err)
		}
		return result
	}

	first := connect("first")
	claim := call(first, "project_coordination_write", map[string]any{
		"action": "claim", "task_id": taskID, "request_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})
	if claim.IsError {
		t.Fatalf("claim failed: %#v", claim)
	}
	claimJSON, _ := json.Marshal(claim)
	if strings.Contains(string(claimJSON), privateContext) {
		t.Fatalf("claim leaked private context: %s", claimJSON)
	}
	if app.Claims.Count() != 1 {
		t.Fatalf("claim count = %d", app.Claims.Count())
	}

	checkpoint := call(first, "project_coordination_write", map[string]any{
		"action": "checkpoint", "run_id": runID, "request_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "summary": "progress",
	})
	if checkpoint.IsError {
		t.Fatalf("checkpoint failed: %#v", checkpoint)
	}
	checkpointJSON, _ := json.Marshal(checkpoint)
	if strings.Contains(string(checkpointJSON), privateContext) {
		t.Fatalf("checkpoint leaked private context: %s", checkpointJSON)
	}

	second := connect("second")
	retired := call(second, "project_coordination_write", map[string]any{
		"action": "claim", "task_id": taskID, "request_id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		"compaction_fingerprint": strings.Repeat("e", 64), "compaction_through": 12,
	})
	if !retired.IsError {
		t.Fatalf("retired compaction fields were accepted: %#v", retired)
	}
	cross := call(second, "project_coordination_write", map[string]any{
		"action": "checkpoint", "run_id": runID, "request_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "summary": "cross-session",
	})
	if !cross.IsError {
		t.Fatalf("cross-session checkpoint succeeded: %#v", cross)
	}
	mu.Lock()
	if checkpointCalls != 1 {
		t.Fatalf("runtime checkpoint calls = %d", checkpointCalls)
	}
	mu.Unlock()

	read := call(second, "project_coordination", map[string]any{"action": "task_context", "task_id": taskID})
	if read.IsError || read.StructuredContent == nil {
		t.Fatalf("coordination read failed: %#v", read)
	}
	readJSON, _ := json.Marshal(read)
	if strings.Contains(string(readJSON), privateContext) {
		t.Fatalf("read leaked private context: %s", readJSON)
	}

	app.Close()
	if app.Claims.Count() != 0 {
		t.Fatalf("claims retained on shutdown: %d", app.Claims.Count())
	}
}
