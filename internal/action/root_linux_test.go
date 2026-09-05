package action

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"loki/internal/process"
	"loki/internal/rpc"
)

func TestRootRunnerActionScope(t *testing.T) {
	if os.Getenv("LOKI_REQUIRE_ROOT_ACTION_TESTS") != "1" {
		t.Skip("explicit development root action acceptance not requested")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root action acceptance requires development root")
	}
	runner := os.Getenv("LOKI_TEST_RUNNER")
	if runner == "" {
		t.Fatal("explicit development runner is required")
	}
	identity, err := user.Lookup(runner)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.ParseUint(identity.Uid, 10, 32)
	if err != nil || uid == 0 {
		t.Fatal("runner must be non-root")
	}
	gid, err := strconv.ParseUint(identity.Gid, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	c, workspace := actionControllerFixture(t, []string{"awk", `BEGIN { print ENVIRON["TOKEN"]; system("id -u"); print "root-runner-scope-ok" }`})
	if err := os.Chown(workspace, int(uid), int(gid)); err != nil {
		t.Fatal(err)
	}
	// testing's outer temporary directory is root-only; service workspace and
	// installed binaries instead have traversable administrator-owned ancestors.
	if err := os.Chmod(filepath.Dir(filepath.Dir(workspace)), 0755); err != nil {
		t.Fatal(err)
	}
	r, err := NewRuntime(c, Layout{Workspace: workspace, Binary: candidateBinary(t), Bwrap: "/usr/bin/bwrap", Runner: runner, UID: uint32(uid), GID: uint32(gid), SystemdScope: true}, process.ManagerOptions{MaxProcesses: 2, MaxOutputBytes: 4096, Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	result, err := r.Run(t.Context(), RunRequest{Profile: "fixture", Action: "check"})
	if err != nil {
		t.Fatal(err)
	}
	id := result["session_id"].(string)
	deadline := time.Now().Add(20 * time.Second)
	for {
		result, err = r.Read(id, nil, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if result["status"] == "exited" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("root scope timeout: %#v", result)
		}
		time.Sleep(20 * time.Millisecond)
	}
	output := result["output"].(string)
	if result["exit_code"] != 0 || !strings.Contains(output, "[REDACTED]\n"+identity.Uid+"\nroot-runner-scope-ok\n") || strings.Contains(output, "synthetic-action-value") {
		t.Fatalf("root action: %#v", result)
	}
	if result["memory_limit_bytes"] != json.Number("4294967296") || result["systemd_unit"] == nil {
		t.Fatalf("scope metadata: %#v", result)
	}
	private := t.TempDir()
	r.layout.MaterializationDirectory = filepath.Join(private, "markers")
	r.layout.MaterializationRecoveryDirectory = filepath.Join(private, "recovery")
	r.layout.SnapshotDirectory = filepath.Join(private, "snapshots")
	for _, path := range []string{r.layout.MaterializationRecoveryDirectory, r.layout.SnapshotDirectory, filepath.Join(workspace, ".tmp")} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, int(uid), int(gid)); err != nil {
			t.Fatal(err)
		}
	}
	r.layout.DockerDirectory = t.TempDir()
	r.layout.DockerSocket = filepath.Join(t.TempDir(), "daemon.sock")
	r.layout.DockerUID = 0
	listener, err := net.Listen("unix", r.layout.DockerSocket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) { io.WriteString(w, "docker-ok") })}
	go server.Serve(listener)
	defer server.Close()
	script := `import os,socket,http.client
from pathlib import Path
p=Path(os.environ['ENV_FILE']);print(p.read_text(),end='')
try:
 p.open('w');raise SystemExit(5)
except OSError: pass
if 'DOCKER_HOST' in os.environ:
 c=http.client.HTTPConnection('localhost');c.sock=socket.socket(socket.AF_UNIX);c.sock.connect(os.environ['DOCKER_HOST'][len('unix://'):]);c.request('GET','/_ping');print(c.getresponse().read().decode());c.close()
print('materialization-ok')
`
	if err := os.WriteFile(filepath.Join(workspace, "scope-check.py"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	for _, docker := range []bool{false, true} {
		for _, fixed := range []bool{false, true} {
			policy := map[string]any{"command": []string{"python3", "scope-check.py"}, "cwd": ".", "secrets": []string{"TOKEN"}, "all_secrets": false, "timeout_seconds": 10, "max_output_bytes": 4096, "materialize_env_file": "ENV_FILE", "docker_access": docker}
			if fixed {
				policy["materialize_env_path"] = "fixed.env"
			}
			data, _ := json.Marshal(policy)
			if _, err := c.SetAction(t.Context(), "fixture", "check", data); err != nil {
				t.Fatal(err)
			}
			started, err := r.Run(t.Context(), RunRequest{Profile: "fixture", Action: "check"})
			if err != nil {
				t.Fatal(err)
			}
			result := completed(t, r.processes, started["session_id"].(string))
			output := result["output"].(string)
			if result["exit_code"] != 0 || result["cleanup_error"] != nil || !strings.Contains(output, "materialization-ok") || !strings.Contains(output, "[REDACTED]") || strings.Contains(output, "synthetic-action-value") || (docker && !strings.Contains(output, "docker-ok")) {
				t.Fatalf("root docker=%v fixed=%v: %#v", docker, fixed, result)
			}
		}
	}
	init := exec.CommandContext(t.Context(), "/usr/sbin/runuser", "-u", runner, "--", "/usr/bin/git", "init", "-q", workspace)
	init.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("fixture repository: %v %s", err, output)
	}
	c.Projects.Runner = runner
	if _, err := c.Register(t.Context(), ".", nil); err != nil {
		t.Fatal(err)
	}
	workflow, _ := json.Marshal(map[string]any{"steps": [][]string{{"fixture", "check"}}, "required_secrets": map[string][]string{"fixture": {"TOKEN"}}, "timeout_seconds": 60})
	if _, err := c.SetWorkflow(t.Context(), ".", "development", workflow); err != nil {
		t.Fatal(err)
	}
	r.layout.RuntimeSocket = filepath.Join(t.TempDir(), "runtime.sock")
	r.layout.RuntimeUID = 0
	rpcListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: r.layout.RuntimeSocket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(r.layout.RuntimeSocket, 0, int(gid)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(r.layout.RuntimeSocket, 0660); err != nil {
		t.Fatal(err)
	}
	serverCtx, cancel := context.WithCancel(t.Context())
	rpcDone := make(chan error, 1)
	runtimeServer := rpc.Server{AgentUID: uint32(uid), Operations: map[string]rpc.Operation{
		"project_workflow": {Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request BootstrapRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, err
			}
			return c.Workflow(ctx, request.CWD, request.Workflow)
		}},
		"run_action": {Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request RunRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, err
			}
			return r.Run(ctx, request)
		}},
		"read_process": {Handle: func(_ context.Context, raw json.RawMessage) (any, error) {
			var request struct {
				ID     string `json:"session_id"`
				Offset *int64
				Limit  int
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, err
			}
			return r.Read(request.ID, request.Offset, request.Limit)
		}},
		"stop_process": {Handle: func(_ context.Context, raw json.RawMessage) (any, error) {
			var request struct {
				ID string `json:"session_id"`
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, err
			}
			return r.Stop(request.ID)
		}},
	}}
	go func() { rpcDone <- runtimeServer.Serve(serverCtx, rpcListener) }()
	defer func() {
		cancel()
		rpcListener.Close()
		if err := <-rpcDone; err != nil {
			t.Error(err)
		}
	}()
	started, err := r.Bootstrap(t.Context(), BootstrapRequest{CWD: ".", Workflow: "development"})
	if err != nil {
		t.Fatal(err)
	}
	result = completed(t, r.processes, started["session_id"].(string))
	output = result["output"].(string)
	if result["exit_code"] != 0 || !strings.Contains(output, "bootstrap: completed fixture/check") || strings.Contains(output, "synthetic-action-value") {
		t.Fatalf("root bootstrap: %#v", result)
	}
}
