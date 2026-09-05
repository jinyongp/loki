package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/action"
	"loki/internal/contract"
	"loki/internal/mcpserver"
	"loki/internal/process"
	"loki/internal/secret"
)

func TestActionSandboxMCP(t *testing.T) {
	probe := exec.CommandContext(t.Context(), "/usr/bin/bwrap", "--unshare-all", "--ro-bind", "/usr", "/usr", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--", "/usr/bin/true")
	if err := probe.Run(); err != nil || os.Getuid() == 0 {
		if os.Getenv("LOKI_REQUIRE_SANDBOX_TESTS") == "1" {
			t.Fatalf("unprivileged sandbox required: %v", err)
		}
		t.Skip("unprivileged bubblewrap namespace unavailable")
	}
	binary := filepath.Join(t.TempDir(), "loki")
	build := exec.CommandContext(t.Context(), filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-trimpath", "-o", binary, "../../cmd/loki")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("candidate build: %v %s", err, output)
	}
	projects, paths := serviceFixture(t)
	c := secret.Controller{StateDirectory: filepath.Join(t.TempDir(), "runtime"), Projects: projects}
	if _, err := c.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateProfile(t.Context(), "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ImportValues(t.Context(), "fixture", map[string]string{"TOKEN": "synthetic-mcp-private"}); err != nil {
		t.Fatal(err)
	}
	for name, program := range map[string]string{
		"check": `BEGIN { print ENVIRON["TOKEN"]; while ((getline flag < "release") < 0) { close("release"); system("sleep 0.01") }; print "finished" }`,
		"slow":  `BEGIN { print "ready"; system("sleep 30") }`,
		"fail":  `BEGIN { exit 7 }`,
	} {
		encoded, _ := json.Marshal(map[string]any{"command": []string{"awk", program}, "cwd": "repo", "secrets": []string{"TOKEN"}, "all_secrets": false, "required_secrets": []string{"TOKEN"}, "timeout_seconds": 10, "max_output_bytes": 4096, "singleton": true})
		if _, err := c.SetAction(t.Context(), "fixture", name, encoded); err != nil {
			t.Fatal(err)
		}
	}
	recovery := filepath.Join(t.TempDir(), "recovery")
	if err := os.Mkdir(recovery, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "runtime.sock")
	actions, err := action.NewRuntime(c, action.Layout{RuntimeSocket: socket, RuntimeUID: uint32(os.Getuid()), Workspace: projects.WorkspaceRoot, Binary: binary, Bwrap: "/usr/bin/bwrap", UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), PreviewBaseDomain: "preview.example.test", MaterializationDirectory: filepath.Join(t.TempDir(), "materializations"), MaterializationRecoveryDirectory: recovery}, process.ManagerOptions{MaxProcesses: 4, MaxOutputBytes: 4096, Retention: time.Minute, StopGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer actions.Close()
	ops := SecretOperations(c)
	for name, operation := range ActionOperations(actions) {
		ops[name] = operation
	}
	client := secretSocket(t, ops, true, socket)
	handlers := map[string]mcpserver.Handler{"action": ActionHandler(client, paths), "bootstrap_project": BootstrapHandler(client, paths)}
	baseline, _ := contract.Baseline()
	definitions, _ := baseline.Definitions()
	// Other tool domains are not covered by this scoped integration test.
	for _, definition := range definitions {
		if handlers[definition.Name] == nil {
			handlers[definition.Name] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return nil, errors.New("outside action sandbox test scope")
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
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "isolated-action-test", Version: "1"}, nil).Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(arguments map[string]any) map[string]any {
		t.Helper()
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "action", Arguments: arguments})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("action error: %v", result.Content)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "synthetic-mcp-private") {
			t.Fatal("secret crossed MCP boundary")
		}
		var value map[string]any
		if err = json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	run := func(name string) map[string]any {
		return call(map[string]any{"operation": "run", "profile": "fixture", "action_name": name, "cwd": "feature"})
	}
	first := run("check")
	second := run("check")
	if second["session_id"] != first["session_id"] || second["reused"] != true || first["cwd"] != "/workspace/feature" {
		t.Fatal("action singleton/worktree binding changed")
	}
	if err = os.WriteFile(filepath.Join(projects.WorkspaceRoot, "feature", "release"), []byte("go\n"), 0600); err != nil {
		t.Fatal(err)
	}
	readComplete := func(id any) map[string]any {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			value := call(map[string]any{"operation": "process", "session_id": id})
			if value["status"] == "exited" {
				return value
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("action did not complete")
		return nil
	}
	completed := readComplete(first["session_id"])
	if completed["outcome"] != "succeeded" || completed["exit_code"] != float64(0) || completed["output"] != "[REDACTED]\nfinished\n" {
		t.Fatalf("completed action: %#v", completed)
	}
	failed := readComplete(run("fail")["session_id"])
	if failed["outcome"] != "failed" || failed["exit_code"] != float64(7) {
		t.Fatalf("failed action: %#v", failed)
	}
	slow := run("slow")
	stopped := call(map[string]any{"operation": "stop", "session_id": slow["session_id"]})
	if stopped["status"] != "exited" || stopped["outcome"] != "stopped" {
		t.Fatalf("stopped action: %#v", stopped)
	}
	listed := call(map[string]any{"operation": "processes"})
	if len(listed["processes"].([]any)) != 3 {
		t.Fatalf("action processes: %#v", listed)
	}
	// Preview orchestration uses private prepare/run RPC. The public action tool
	// continues to expose its frozen schema; proxy assembly is a separate gate.
	encoded, _ := json.Marshal(map[string]any{"command": []string{"awk", `BEGIN { print ENVIRON["ORIGIN"]; print ENVIRON["PORT"]; print ARGV[1]; exit }`, "{LOKI_PORT}"}, "cwd": "repo", "secrets": []string{"TOKEN"}, "all_secrets": false, "timeout_seconds": 10, "max_output_bytes": 4096,
		"dynamic_port": map[string]any{"preferred": 41280, "environment": "PORT", "origin_environment": "ORIGIN"}})
	if _, err = c.SetAction(t.Context(), "fixture", "web", encoded); err != nil {
		t.Fatal(err)
	}
	rpcCall := func(request map[string]any) map[string]any {
		t.Helper()
		data, err := client.Call(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err = json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	prepared := rpcCall(map[string]any{"operation": "prepare_action", "profile": "fixture", "action_name": "web", "cwd": "feature"})
	url := "https://loki-" + strings.Repeat("b", 32) + ".preview.example.test"
	dynamic := rpcCall(map[string]any{"operation": "run_action", "profile": "fixture", "action_name": "web", "cwd": "feature", "launch_token": prepared["launch_token"], "public_environment": map[string]string{"ORIGIN": url}})
	finished := readComplete(dynamic["session_id"])
	port := strconv.Itoa(int(prepared["port"].(float64)))
	if finished["exit_code"] != float64(0) || finished["output"] != url+"\n"+port+"\n"+port+"\n" || finished["port"] != prepared["port"] || finished["cwd"] != "/workspace/feature" {
		t.Fatalf("prepared RPC launch: %#v", finished)
	}
	// The reference delegates prepare/run to the agent UID. Administrative
	// configuration requires the MCP cgroup; action namespaces omit the control
	// socket entirely. Do not conflate these distinct authorization boundaries.
	agent := secretSocket(t, ops, false)
	if _, err := agent.Call(t.Context(), map[string]any{"operation": "prepare_action", "profile": "fixture", "action_name": "web"}); err != nil {
		t.Fatalf("agent UID could not prepare a registered action: %v", err)
	}
	encoded, _ = json.Marshal(map[string]any{"command": []string{"pwd"}, "cwd": "repo", "secrets": []string{"TOKEN"}, "all_secrets": false, "required_secrets": []string{"TOKEN"}, "timeout_seconds": 10, "max_output_bytes": 4096, "materialize_env_file": "ENV_FILE", "materialize_env_path": "fixture.env"})
	if _, err := c.SetAction(t.Context(), "fixture", "cleanup", encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ImportValues(t.Context(), "fixture", map[string]string{"TOKEN": ""}); err != nil {
		t.Fatal(err)
	}
	// Cleanup must work even when required runtime credentials are no longer set.
	if _, err := c.Register(t.Context(), "repo", nil); err != nil {
		t.Fatal(err)
	}
	setWorkflow := func(name string, steps [][]string) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"steps": steps, "required_secrets": map[string][]string{"fixture": {"TOKEN"}}, "timeout_seconds": 60})
		if _, err := c.SetWorkflow(t.Context(), "repo", name, data); err != nil {
			t.Fatal(err)
		}
	}
	setWorkflow("development", [][]string{{"fixture", "check"}, {"fixture", "fail"}})
	bootstrap := func() map[string]any {
		t.Helper()
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "bootstrap_project", Arguments: map[string]any{"cwd": "feature"}})
		if err != nil || result.IsError {
			t.Fatalf("bootstrap: %v %#v", err, result)
		}
		data, _ := json.Marshal(result.StructuredContent)
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	blocked := bootstrap()
	if blocked["accepted"] != false || blocked["status"] != "blocked" || blocked["session_id"] != nil {
		t.Fatalf("blocked bootstrap: %#v", blocked)
	}
	if _, err := c.ImportValues(t.Context(), "fixture", map[string]string{"TOKEN": "synthetic-mcp-private"}); err != nil {
		t.Fatal(err)
	}
	started := bootstrap()
	if started["accepted"] != true || started["configuration_ready"] != true || started["cwd"] != "feature" {
		t.Fatal(started)
	}
	result := readComplete(started["session_id"])
	if result["exit_code"] != float64(7) || !strings.Contains(result["output"].(string), "bootstrap: completed fixture/check") {
		t.Fatalf("workflow failure: %#v", result)
	}
	setWorkflow("development", [][]string{{"fixture", "check"}})
	result = readComplete(bootstrap()["session_id"])
	if result["exit_code"] != float64(0) {
		t.Fatalf("workflow success: %#v", result)
	}
	setWorkflow("development", [][]string{{"fixture", "slow"}})
	started = bootstrap()
	deadline := time.Now().Add(5 * time.Second)
	for {
		active := false
		for _, item := range actions.List()["processes"].([]map[string]any) {
			if item["name"] == "fixture/slow" && item["status"] == "running" {
				active = true
			}
		}
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("workflow action did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	call(map[string]any{"operation": "stop", "session_id": started["session_id"]})
	for _, item := range actions.List()["processes"].([]map[string]any) {
		if item["status"] == "running" {
			t.Fatalf("workflow left a running action: %#v", item)
		}
	}
	if _, err := c.ImportValues(t.Context(), "fixture", map[string]string{"TOKEN": ""}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects.WorkspaceRoot, "repo", "fixture.env"), []byte("synthetic stale file"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{true, false} {
		value := call(map[string]any{"operation": "clear_materialization", "profile": "fixture", "action_name": "cleanup"})
		if value["cleared"] != want {
			t.Fatalf("MCP explicit cleanup: %#v", value)
		}
	}
}
