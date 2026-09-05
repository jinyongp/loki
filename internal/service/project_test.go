package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/contract"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/project"
	"loki/internal/rpc"
)

func serviceFixture(t *testing.T) (*project.Store, *policy.Workspace) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "initial"}, {"-C", repo, "worktree", "add", "-q", "-b", "feature", filepath.Join(workspace, "feature")}} {
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Env = []string{"HOME=" + root, "PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s %v", out, err)
		}
	}
	store, err := project.New(workspace, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	paths, err := policy.New(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { paths.Close() })
	return store, paths
}
func TestProjectTaskMCPUnixSocket(t *testing.T) {
	binary := "/home/linuxbrew/.linuxbrew/bin/task"
	if _, err := os.Stat(binary); err != nil {
		t.Skip("Taskwarrior unavailable")
	}
	store, paths := serviceFixture(t)
	ops := ProjectStateOperations(store, project.Tasks{Store: store, Binary: binary, Home: t.TempDir()})
	for name, op := range ops {
		if op.Permission != rpc.Agent {
			t.Fatalf("delegated permission changed: %s", name)
		}
	}
	socket := filepath.Join(t.TempDir(), "runtime.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	server := rpc.Server{AgentUID: uint32(os.Getuid()), Operations: ops}
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	uid := uint32(os.Getuid())
	client := rpc.Client{Socket: socket, ExpectedUID: &uid}
	handlers := ProjectHandlers(client, paths)
	baseline, _ := contract.Baseline()
	defs, _ := baseline.Definitions()
	// Other domains deliberately fail if invoked; they are not parity evidence.
	for _, def := range defs {
		if handlers[def.Name] == nil {
			handlers[def.Name] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return nil, errors.New("unexpected out-of-scope test tool")
			}
		}
	}
	mcpServer, err := mcpserver.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	ss, err := mcpServer.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "isolated-test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("%s: %v", name, result.Content)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err = json.Unmarshal(encoded, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	expectError := func(name string, args map[string]any, message string) {
		t.Helper()
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(result.Content)
		if !result.IsError || !strings.Contains(string(data), message) {
			t.Fatalf("wanted %q: %s", message, data)
		}
	}
	expectError("task_inspect", map[string]any{"cwd": "repo"}, "TASK_NOT_INITIALIZED")
	initialized := call("project", map[string]any{"cwd": "/workspace/repo", "action": "init", "goal": "MCP shared task queue", "slug_base": "mcp-task-queue"})
	slug := initialized["slug"].(string)
	for _, arguments := range []any{nil, []any{nil}, []string{"exec", "/usr/bin/true"}, []string{"rc:/dev/null"}} {
		if _, err := client.Call(ctx, map[string]any{"operation": "project_task", "cwd": "repo", "arguments": arguments}); err == nil {
			t.Fatalf("invalid raw arguments accepted: %v", arguments)
		}
	}
	rawResult, err := client.Call(ctx, map[string]any{"operation": "project_task", "cwd": "repo", "arguments": []string{"count"}})
	if err != nil || !strings.Contains(string(rawResult), `"exit_code":0`) {
		t.Fatalf("raw task socket: %s %v", rawResult, err)
	}
	created := call("project", map[string]any{"cwd": "repo", "action": "write", "filename": "plan.md", "content": "initial plan\n"})
	read := call("project", map[string]any{"cwd": "repo", "action": "read", "filename": "plan.md"})
	if read["sha256"] != created["sha256"] || read["content"] != "initial plan\n" {
		t.Fatalf("artifact %v %v", created, read)
	}
	expectError("project", map[string]any{"cwd": "repo", "action": "read"}, "filename is required for project_state action=read")
	expectError("project", map[string]any{"cwd": "repo", "action": "write", "filename": "plan.md", "content": ""}, "content is required or too large")
	first := call("task_write", map[string]any{"cwd": "repo", "action": "add", "fields": map[string]any{"description": "socket task"}})
	uuid := first["task"].(map[string]any)["uuid"]
	expectError("task_inspect", map[string]any{"cwd": "feature"}, "TASK_WORKSTREAM_REQUIRED")
	call("project", map[string]any{"cwd": "feature", "action": "bind", "workstream": slug})
	listed := call("task_inspect", map[string]any{"cwd": "feature", "action": "next"})
	if listed["count"] != float64(1) || listed["project_id"] != initialized["project_id"] {
		t.Fatalf("shared queue %v", listed)
	}
	deleted := call("task_delete", map[string]any{"cwd": "feature", "uuid": uuid})
	if deleted["task"].(map[string]any)["status"] != "deleted" {
		t.Fatalf("delete %v", deleted)
	}
	expectError("task_inspect", map[string]any{"cwd": "../outside"}, "parent traversal")
	for _, cwd := range []any{nil, "", "/workspace/repo", "../outside", 17} {
		if _, err = client.Call(ctx, map[string]any{"operation": "project_state", "action": "status", "cwd": cwd}); err == nil || !strings.Contains(err.Error(), "workspace-relative") {
			t.Fatalf("RPC cwd %v: %v", cwd, err)
		}
	}
}

type recordingRuntime struct {
	Request map[string]any
	Calls   int
}

func (r *recordingRuntime) Call(_ context.Context, request any) (json.RawMessage, error) {
	r.Calls++
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &r.Request)
	return json.RawMessage(`{"ok":true}`), err
}
func TestProjectWorkflowDispatch(t *testing.T) {
	_, paths := serviceFixture(t)
	client := &recordingRuntime{}
	handler := ProjectHandlers(client, paths)["project"]
	_, err := handler(t.Context(), map[string]any{"action": "set_workflow", "cwd": "repo", "workflow": "dev", "steps": []string{"web/start", "api/dev"}, "required_secrets": []string{"web/TOKEN", "api/KEY"}, "timeout_seconds": 3600})
	if err != nil {
		t.Fatal(err)
	}
	if client.Request["operation"] != "project_set_workflow" || !reflect.DeepEqual(client.Request["steps"], []any{[]any{"web", "start"}, []any{"api", "dev"}}) {
		t.Fatalf("workflow request %v", client.Request)
	}
	for _, steps := range [][]string{nil, {"invalid"}, {"/start"}, {"web/start/extra"}} {
		_, err = handler(t.Context(), map[string]any{"action": "set_workflow", "cwd": "repo", "workflow": "dev", "steps": steps})
		if err == nil {
			t.Fatalf("bad steps %v", steps)
		}
	}
	if client.Calls != 1 {
		t.Fatal("invalid workflow reached runtime")
	}
}
