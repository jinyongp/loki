package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/agentcontext"
	"loki/internal/config"
	"loki/internal/portguard"
)

func TestProjectContextRecoversAcrossMCPRestartAfterPostCheckpointChange(t *testing.T) {
	base, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	base.Root = t.TempDir()
	base.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	repo := filepath.Join(base.Root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "/usr/bin/git", args...)
		cmd.Dir = repo
		cmd.Env = env
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	runGit("init", "-q", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("resume rules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "tracked.go"), []byte("package repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "--", "AGENTS.md", "src/tracked.go")
	runGit("-c", "user.name=Loki Test", "-c", "user.email=loki@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "base")

	const (
		taskID       = "71111111-1111-4111-8111-111111111111"
		runID        = "72222222-2222-4222-8222-222222222222"
		workstreamID = "73333333-3333-4333-8333-333333333333"
		validationID = "74444444-4444-4444-8444-444444444444"
	)
	journal, err := agentcontext.NewContextJournal(filepath.Join(t.TempDir(), "context"), agentcontext.ContextJournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	lostCheckpointResponse := false
	runtime := runtimeFixture(func(ctx context.Context, request any) (json.RawMessage, error) {
		row := request.(map[string]any)
		switch row["operation"] {
		case "devtools_task_current":
			raw, _ := json.Marshal(map[string]any{
				"profile": "fixture", "revision": 20,
				"items": []any{map[string]any{
					"id": runID, "task_id": taskID, "workstream_id": workstreamID, "directory": "repo/src",
				}},
			})
			return raw, nil
		case "devtools_task_context":
			raw, _ := json.Marshal(map[string]any{
				"profile": "fixture", "revision": 21,
				"item":        map[string]any{"id": taskID, "workstream_id": workstreamID},
				"validations": []any{map[string]any{"id": validationID}},
				"tasks":       []any{}, "history": []any{}, "omitted_ids": []string{},
			})
			return raw, nil
		case "devtools_coordination_mutate":
			raw, _ := json.Marshal(map[string]any{
				"public":  map[string]any{"profile": "fixture", "revision": 22, "claimed": true},
				"context": "restart-private-context", "context_valid": true,
				"run_id": runID, "task_id": taskID, "profile": "fixture",
			})
			return raw, nil
		case "context_checkpoint_latest":
			latest, err := journal.Latest(ctx, agentcontext.ContextScope{
				RepositoryID: row["repository_id"].(string), WorktreeID: row["worktree_id"].(string),
				WorkstreamID: row["workstream_id"].(string),
			})
			if err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(latest)
			return raw, nil
		case "context_checkpoint_put":
			encoded, _ := json.Marshal(row["draft"])
			var draft agentcontext.ContextDraft
			if err := json.Unmarshal(encoded, &draft); err != nil {
				return nil, err
			}
			requestID := row["request_id"].(string)
			put, err := journal.Put(ctx, requestID, row["expected_previous"].(string), draft)
			if err != nil {
				return nil, err
			}
			if requestID == "7ddddddd-dddd-4ddd-8ddd-dddddddddddd" && !lostCheckpointResponse {
				lostCheckpointResponse = true
				return nil, errors.New("synthetic terminal response lost")
			}
			raw, _ := json.Marshal(put)
			return raw, nil
		default:
			return json.RawMessage(`{"initialized":true,"in_use":false,"listeners":[]}`), nil
		}
	})
	browser := browserFixture(func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"status": "running"}, nil
	})
	token := strings.Repeat("r", 43)

	start := func(name string) (*MCPApp, *httptest.Server, *mcp.ClientSession) {
		t.Helper()
		server := httptest.NewUnstartedServer(nil)
		_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
		c := base
		c.Port, _ = strconv.Atoi(portText)
		ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
		if err != nil {
			t.Fatal(err)
		}
		app, err := NewMCP(c, MCPOptions{
			Runtime: runtime, PortGuard: runtime, Browser: browser, Jobs: jobControllerFixture(), GitRunner: gitRunnerFixture(t, c, nil), Ports: ports,
			Policy: policyGenerationFixture(t), Token: token,
			Environment: map[string]string{
				"HOME": t.TempDir(), "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		server.Config.Handler = app
		server.Start()
		client, err := mcp.NewClient(&mcp.Implementation{Name: name, Version: "1"}, nil).Connect(
			t.Context(),
			&mcp.StreamableClientTransport{
				Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}},
				DisableStandaloneSSE: true,
			},
			nil,
		)
		if err != nil {
			app.Close()
			server.Close()
			t.Fatal(err)
		}
		return app, server, client
	}

	readArgs := map[string]any{"cwd": "repo", "target": "src/new.go"}
	app1, server1, client1 := start("before-restart")
	before, call := projectContextResult(t, client1, "project_context", readArgs)
	if call.IsError || before["transition"].(map[string]any)["action"] != "takeover" {
		t.Fatalf("before restart context = %#v call=%#v", before, call)
	}
	takeover, err := client1.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "project_coordination_write",
		Arguments: map[string]any{
			"action": "takeover", "task_id": taskID, "expected_run_id": runID,
			"request_id": "7aaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		},
	})
	if err != nil || takeover.IsError {
		t.Fatalf("first takeover = %#v err=%v", takeover, err)
	}
	owned, call := projectContextResult(t, client1, "project_context", readArgs)
	if call.IsError {
		t.Fatalf("owned context = %#v", call)
	}
	firstBasis := owned["basis_fingerprint"].(string)
	written, call := projectContextResult(t, client1, "project_context_write", map[string]any{
		"cwd": "repo", "target": "src/new.go",
		"request_id":     "7bbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"expected_basis": firstBasis, "expected_previous": "missing",
		"summary":     "checkpoint before abrupt loss",
		"remaining":   []string{"finish after restart"},
		"next_action": "resume from fresh session",
	})
	if call.IsError {
		t.Fatalf("first checkpoint = %#v", call)
	}
	firstRecord := written["record"].(map[string]any)["id"].(string)

	client1.Close()
	app1.Close()
	server1.Close()

	if err := os.WriteFile(filepath.Join(repo, "src", "tracked.go"), []byte("package repo\n// post-checkpoint change\n"), 0644); err != nil {
		t.Fatal(err)
	}

	app2, server2, client2 := start("after-restart")
	defer client2.Close()
	defer app2.Close()
	defer server2.Close()
	after, call := projectContextResult(t, client2, "project_context", readArgs)
	if call.IsError {
		t.Fatalf("after restart context = %#v", call)
	}
	if after["transition"].(map[string]any)["action"] != "takeover" {
		t.Fatalf("after restart transition = %#v", after["transition"])
	}
	checkpoint := after["checkpoint"].(map[string]any)
	if checkpoint["found"] != true || checkpoint["stale"] != true || checkpoint["expected_previous"] != firstRecord {
		t.Fatalf("after restart checkpoint = %#v", checkpoint)
	}
	reasons := map[string]bool{}
	for _, raw := range checkpoint["stale_reasons"].([]any) {
		reasons[raw.(string)] = true
	}
	if !reasons["code"] {
		t.Fatalf("post-checkpoint worktree change was not detected: %#v", checkpoint["stale_reasons"])
	}
	basis := after["basis"].(map[string]any)
	validations := basis["validation_record_ids"].([]any)
	if len(validations) != 1 || validations[0] != validationID {
		t.Fatalf("validation evidence was not rehydrated: %#v", validations)
	}

	secondTakeover, err := client2.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "project_coordination_write",
		Arguments: map[string]any{
			"action": "takeover", "task_id": taskID, "expected_run_id": runID,
			"request_id": "7ccccccc-cccc-4ccc-8ccc-cccccccccccc",
		},
	})
	if err != nil || secondTakeover.IsError {
		t.Fatalf("second takeover = %#v err=%v", secondTakeover, err)
	}
	current, call := projectContextResult(t, client2, "project_context", readArgs)
	if call.IsError {
		t.Fatalf("current context after takeover = %#v", call)
	}
	secondBasis := current["basis_fingerprint"].(string)
	secondWriteArgs := map[string]any{
		"cwd": "repo", "target": "src/new.go",
		"request_id":     "7ddddddd-dddd-4ddd-8ddd-dddddddddddd",
		"expected_basis": secondBasis, "expected_previous": firstRecord,
		"summary":     "reconciled after fresh session",
		"remaining":   []string{"continue implementation"},
		"next_action": "continue from reconciled worktree",
	}
	_, lostCall := projectContextResult(t, client2, "project_context_write", secondWriteArgs)
	if !lostCall.IsError {
		t.Fatalf("synthetic lost terminal response did not surface as an uncertain call: %#v", lostCall)
	}
	secondWrite, call := projectContextResult(t, client2, "project_context_write", secondWriteArgs)
	if call.IsError || secondWrite["record"] == nil || secondWrite["replayed"] != true {
		t.Fatalf("replayed checkpoint = %#v call=%#v", secondWrite, call)
	}
}
