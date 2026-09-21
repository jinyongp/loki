package service

import (
	"context"
	"encoding/json"
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

func projectContextResult(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s transport: %v", name, err)
	}
	if result.IsError {
		return nil, result
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value, result
}

func TestProjectContextResumesAndWritesSessionOwnedCheckpoint(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	repo := filepath.Join(c.Root, "repo")
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
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("root rules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "tracked.go"), []byte("package repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(repo, ".agents", "skills", "review-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review-skill\ndescription: Review repository changes.\n---\n\n# Review\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "--", "AGENTS.md", "src/tracked.go", ".agents/skills/review-skill/SKILL.md")
	runGit("-c", "user.name=Loki Test", "-c", "user.email=loki@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "base")

	const (
		taskID       = "11111111-1111-4111-8111-111111111111"
		runID        = "22222222-2222-4222-8222-222222222222"
		workstreamID = "33333333-3333-4333-8333-333333333333"
		validationID = "44444444-4444-4444-8444-444444444444"
		privateClaim = "private-project-context-claim"
	)
	journal, err := agentcontext.NewContextJournal(filepath.Join(t.TempDir(), "context"), agentcontext.ContextJournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	taskRevision := 11
	runtime := runtimeFixture(func(ctx context.Context, request any) (json.RawMessage, error) {
		row := request.(map[string]any)
		switch row["operation"] {
		case "devtools_task_current":
			if row["cwd"] != "repo/src" {
				t.Fatalf("task current cwd = %#v", row["cwd"])
			}
			raw, _ := json.Marshal(map[string]any{
				"profile": "fixture", "revision": 10,
				"items": []any{map[string]any{
					"id": runID, "task_id": taskID, "workstream_id": workstreamID, "directory": "repo/src",
				}},
			})
			return raw, nil
		case "devtools_task_context":
			raw, _ := json.Marshal(map[string]any{
				"profile": "fixture", "revision": taskRevision,
				"item": map[string]any{
					"id": taskID, "kind": "task", "workstream_id": workstreamID,
				},
				"documents": map[string]any{"plan": map[string]any{"body": "untrusted plan text"}},
				"tasks":     []any{}, "history": []any{},
				"validations": []any{map[string]any{"id": validationID}},
				"truncated":   false, "omitted_ids": []string{},
			})
			return raw, nil
		case "devtools_coordination_mutate":
			if row["action"] != "task takeover" || row["target_id"] != taskID || row["expected_run_id"] != runID {
				t.Fatalf("takeover request = %#v", row)
			}
			raw, _ := json.Marshal(map[string]any{
				"public":  map[string]any{"profile": "fixture", "revision": 12, "claimed": true},
				"context": privateClaim, "context_valid": true, "run_id": runID, "task_id": taskID, "profile": "fixture",
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
			result, err := journal.Put(ctx, row["request_id"].(string), row["expected_previous"].(string), draft)
			if err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(result)
			return raw, nil
		default:
			return json.RawMessage(`{"initialized":true,"in_use":false,"listeners":[]}`), nil
		}
	})
	browser := browserFixture(func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"status": "running"}, nil
	})
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	c.Port, _ = strconv.Atoi(portText)
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("p", 43)
	app, err := NewMCP(c, MCPOptions{
		Runtime: runtime, PortGuard: runtime, Browser: browser, Jobs: jobControllerFixture(), GitJobs: gitJobsFixture(t, c, nil), Ports: ports,
		Policy: policyGenerationFixture(t), Token: token,
		Environment: map[string]string{
			"HOME": t.TempDir(), "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server.Config.Handler = app
	server.Start()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "project-context-test", Version: "1"}, nil).Connect(
		t.Context(),
		&mcp.StreamableClientTransport{
			Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}},
			DisableStandaloneSSE: true,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	readArgs := map[string]any{
		"cwd": "repo", "target": "src/new.go", "skills": []string{"review-skill"},
	}
	first, result := projectContextResult(t, client, "project_context", readArgs)
	if result.IsError {
		t.Fatalf("project_context failed: %#v", result)
	}
	if first["ambiguous"] != false {
		t.Fatalf("first context ambiguous = %#v", first)
	}
	transition := first["transition"].(map[string]any)
	if transition["action"] != "takeover" || transition["expected_run_id"] != runID {
		t.Fatalf("transition = %#v", transition)
	}
	basis := first["basis"].(map[string]any)
	if basis["task_id"] != taskID || basis["run_id"] != runID || basis["workstream_id"] != workstreamID ||
		len(basis["repository_id"].(string)) != 64 || len(basis["worktree_id"].(string)) != 64 ||
		len(basis["code_basis"].(string)) != 64 {
		t.Fatalf("basis = %#v", basis)
	}
	checkpoint := first["checkpoint"].(map[string]any)
	if checkpoint["found"] != false || checkpoint["expected_previous"] != "missing" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	basisFingerprint := first["basis_fingerprint"].(string)

	_, beforeOwnership := projectContextResult(t, client, "project_context_write", map[string]any{
		"cwd": "repo", "target": "src/new.go", "skills": []string{"review-skill"},
		"request_id":     "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"expected_basis": basisFingerprint, "expected_previous": "missing",
		"summary": "progress", "next_action": "continue",
	})
	if !beforeOwnership.IsError {
		t.Fatalf("checkpoint before takeover succeeded: %#v", beforeOwnership)
	}

	takeover, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "project_coordination_write",
		Arguments: map[string]any{
			"action": "takeover", "task_id": taskID, "expected_run_id": runID,
			"request_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		},
	})
	if err != nil || takeover.IsError {
		t.Fatalf("takeover = %#v err=%v", takeover, err)
	}

	second, result := projectContextResult(t, client, "project_context", readArgs)
	if result.IsError {
		t.Fatalf("owned project_context failed: %#v", result)
	}
	if second["transition"].(map[string]any)["action"] != "none" ||
		second["basis_fingerprint"] != basisFingerprint {
		t.Fatalf("owned context = %#v", second)
	}

	written, result := projectContextResult(t, client, "project_context_write", map[string]any{
		"cwd": "repo", "target": "src/new.go", "skills": []string{"review-skill"},
		"request_id":     "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"expected_basis": basisFingerprint, "expected_previous": "missing",
		"summary": "progress", "decisions": []string{"keep native context"},
		"remaining": []string{"finish resume"}, "next_action": "continue implementation", "blockers": []string{},
	})
	if result.IsError {
		t.Fatalf("project_context_write failed: %#v", result)
	}
	record := written["record"].(map[string]any)
	if len(record["id"].(string)) != 64 || len(record["session_ref"].(string)) != 16 {
		t.Fatalf("written record = %#v", record)
	}
	encoded, _ := json.Marshal(written)
	if strings.Contains(string(encoded), privateClaim) {
		t.Fatalf("project context leaked private claim: %s", encoded)
	}

	afterWrite, result := projectContextResult(t, client, "project_context", readArgs)
	if result.IsError {
		t.Fatalf("context after write failed: %#v", result)
	}
	stored := afterWrite["checkpoint"].(map[string]any)
	if stored["found"] != true || stored["stale"] != false || stored["expected_previous"] != record["id"] {
		t.Fatalf("stored checkpoint = %#v", stored)
	}

	secondClient, err := mcp.NewClient(&mcp.Implementation{Name: "project-context-second", Version: "1"}, nil).Connect(
		t.Context(),
		&mcp.StreamableClientTransport{
			Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}},
			DisableStandaloneSSE: true,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer secondClient.Close()
	secondRead, result := projectContextResult(t, secondClient, "project_context", readArgs)
	if result.IsError || secondRead["transition"].(map[string]any)["action"] != "takeover" {
		t.Fatalf("second session context = %#v result=%#v", secondRead, result)
	}
	secondTakeover, err := secondClient.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "project_coordination_write",
		Arguments: map[string]any{
			"action": "takeover", "task_id": taskID, "expected_run_id": runID,
			"request_id": "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		},
	})
	if err != nil || secondTakeover.IsError {
		t.Fatalf("second takeover = %#v err=%v", secondTakeover, err)
	}
	if app.Claims.count() != 1 {
		t.Fatalf("takeover did not transfer exclusive Run ownership: %d", app.Claims.count())
	}
	_, formerOwnerWrite := projectContextResult(t, client, "project_context_write", map[string]any{
		"cwd": "repo", "target": "src/new.go", "skills": []string{"review-skill"},
		"request_id":     "ffffffff-ffff-4fff-8fff-ffffffffffff",
		"expected_basis": basisFingerprint, "expected_previous": record["id"],
		"summary": "old owner", "next_action": "must fail",
	})
	if !formerOwnerWrite.IsError {
		t.Fatalf("former Run owner wrote context after takeover: %#v", formerOwnerWrite)
	}

	taskRevision = 13
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("changed rules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review-skill\ndescription: Review changed repository state.\n---\n\n# Review changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "tracked.go"), []byte("package repo\n// changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "untracked.txt"), []byte("untracked handoff content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stale, result := projectContextResult(t, client, "project_context", readArgs)
	if result.IsError {
		t.Fatalf("stale context failed: %#v", result)
	}
	staleCheckpoint := stale["checkpoint"].(map[string]any)
	if staleCheckpoint["stale"] != true || stale["basis_fingerprint"] == basisFingerprint || stale["complete"] != false {
		t.Fatalf("stale checkpoint = %#v complete=%#v", staleCheckpoint, stale["complete"])
	}
	reasons := staleCheckpoint["stale_reasons"].([]any)
	reasonSet := map[string]bool{}
	for _, reason := range reasons {
		reasonSet[reason.(string)] = true
	}
	if !reasonSet["guidance"] || !reasonSet["skills"] || !reasonSet["coordination"] || !reasonSet["code"] || !reasonSet["current_evidence_incomplete"] {
		t.Fatalf("stale reasons = %#v", reasons)
	}
	_, oldBasisWrite := projectContextResult(t, client, "project_context_write", map[string]any{
		"cwd": "repo", "target": "src/new.go", "skills": []string{"review-skill"},
		"request_id":     "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		"expected_basis": basisFingerprint, "expected_previous": record["id"],
		"summary": "stale", "next_action": "should fail",
	})
	if !oldBasisWrite.IsError {
		t.Fatalf("stale basis write succeeded: %#v", oldBasisWrite)
	}
}

func TestProjectContextCoordinationChoosesClaimAndDetectsAmbiguity(t *testing.T) {
	const (
		taskA        = "11111111-1111-4111-8111-111111111111"
		taskB        = "22222222-2222-4222-8222-222222222222"
		taskC        = "66666666-6666-4666-8666-666666666666"
		runA         = "33333333-3333-4333-8333-333333333333"
		runB         = "44444444-4444-4444-8444-444444444444"
		workstreamID = "55555555-5555-4555-8555-555555555555"
	)
	t.Run("claim next work", func(t *testing.T) {
		runtime := runtimeFixture(func(_ context.Context, request any) (json.RawMessage, error) {
			row := request.(map[string]any)
			switch row["operation"] {
			case "devtools_task_current":
				raw, _ := json.Marshal(map[string]any{"profile": "fixture", "revision": 1, "items": []any{}})
				return raw, nil
			case "devtools_task_next":
				raw, _ := json.Marshal(map[string]any{
					"profile": "fixture", "revision": 2,
					"item": map[string]any{"id": taskA, "workstream_id": workstreamID},
				})
				return raw, nil
			case "devtools_task_context":
				raw, _ := json.Marshal(map[string]any{
					"profile": "fixture", "revision": 3,
					"item":        map[string]any{"id": taskA, "workstream_id": workstreamID},
					"validations": []any{}, "history": []any{}, "tasks": []any{}, "omitted_ids": []string{},
				})
				return raw, nil
			default:
				t.Fatalf("unexpected operation: %#v", row)
				return nil, nil
			}
		})
		controller := &ProjectContextController{Runtime: runtime, Claims: NewDevtoolsSessionClaims()}
		_, taskID, runID, gotWorkstream, transition, ambiguous, _, gaps, _, err :=
			controller.resolveCoordination(t.Context(), "repo", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if ambiguous || taskID != taskA || runID != "" || gotWorkstream != workstreamID ||
			transition.Action != "claim" || len(gaps) != 0 {
			t.Fatalf("claim resolution task=%s run=%s workstream=%s transition=%#v ambiguous=%v gaps=%#v",
				taskID, runID, gotWorkstream, transition, ambiguous, gaps)
		}
	})

	t.Run("multiple current runs are ambiguous unless task disambiguates", func(t *testing.T) {
		runtime := runtimeFixture(func(_ context.Context, request any) (json.RawMessage, error) {
			row := request.(map[string]any)
			switch row["operation"] {
			case "devtools_task_current":
				raw, _ := json.Marshal(map[string]any{
					"profile": "fixture", "revision": 4,
					"items": []any{
						map[string]any{"id": runA, "task_id": taskA, "workstream_id": workstreamID},
						map[string]any{"id": runB, "task_id": taskB, "workstream_id": workstreamID},
					},
				})
				return raw, nil
			case "devtools_task_context":
				raw, _ := json.Marshal(map[string]any{
					"profile": "fixture", "revision": 5,
					"item":        map[string]any{"id": row["task_id"], "workstream_id": workstreamID},
					"validations": []any{}, "history": []any{}, "tasks": []any{}, "omitted_ids": []string{},
				})
				return raw, nil
			default:
				t.Fatalf("unexpected operation: %#v", row)
				return nil, nil
			}
		})
		controller := &ProjectContextController{Runtime: runtime, Claims: NewDevtoolsSessionClaims()}
		_, _, _, _, transition, ambiguous, _, gaps, _, err :=
			controller.resolveCoordination(t.Context(), "repo", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if !ambiguous || transition.Action != "inspect" {
			t.Fatalf("ambiguous resolution transition=%#v ambiguous=%v gaps=%#v", transition, ambiguous, gaps)
		}
		found := false
		for _, gap := range gaps {
			if gap == "coordination.current.ambiguous" {
				found = true
			}
		}
		if !found {
			t.Fatalf("ambiguous gap missing: %#v", gaps)
		}

		_, taskID, runID, _, disambiguatedTransition, disambiguatedAmbiguous, _, _, _, err :=
			controller.resolveCoordination(t.Context(), "repo", taskA, "")
		if err != nil {
			t.Fatal(err)
		}
		if disambiguatedAmbiguous || taskID != taskA || runID != runA || disambiguatedTransition.Action != "takeover" {
			t.Fatalf("disambiguated resolution task=%s run=%s transition=%#v ambiguous=%v",
				taskID, runID, disambiguatedTransition, disambiguatedAmbiguous)
		}

		_, _, _, _, mismatchTransition, mismatchAmbiguous, _, mismatchGaps, _, err :=
			controller.resolveCoordination(t.Context(), "repo", taskC, "")
		if err != nil {
			t.Fatal(err)
		}
		if !mismatchAmbiguous || mismatchTransition.Action != "inspect" {
			t.Fatalf("mismatched explicit task transition=%#v ambiguous=%v gaps=%#v", mismatchTransition, mismatchAmbiguous, mismatchGaps)
		}
		foundMismatch := false
		for _, gap := range mismatchGaps {
			if gap == "coordination.current.task_mismatch" {
				foundMismatch = true
			}
		}
		if !foundMismatch {
			t.Fatalf("task mismatch gap missing: %#v", mismatchGaps)
		}
	})
}
