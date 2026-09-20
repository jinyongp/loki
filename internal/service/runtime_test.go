package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/config"
	"loki/internal/devtools"
	"loki/internal/execution"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/rpc"
	"loki/internal/secret"
)

func TestRuntimeRoleSocketLifecycle(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	socket := filepath.Join(root, "socket", "control.sock")
	contractRaw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "go", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := execution.Load(contractRaw)
	if err != nil {
		t.Fatal(err)
	}
	runnerState := filepath.Join(root, "runner")
	runnerCache := filepath.Join(root, "cache")
	runnerTemp := filepath.Join(root, "temp")
	githubTemp := filepath.Join(root, "github-temp")
	snapshotDirectory := filepath.Join(root, "snapshots")
	directories := map[string]string{
		"runner-state": runnerState, "runner-config": filepath.Join(root, "config"),
		"runner-gh-config": filepath.Join(root, "gh"), "runner-data": filepath.Join(root, "data"),
		"runner-xdg-state": filepath.Join(root, "xdg-state"), "snapshots": snapshotDirectory,
		"runner-cache": runnerCache, "runner-npm-cache": filepath.Join(root, "npm"),
		"runner-pnpm-store": filepath.Join(root, "pnpm"), "runner-playwright-cache": filepath.Join(root, "playwright"),
		"runner-go-build-cache": filepath.Join(root, "go-build"), "runner-go-mod-cache": filepath.Join(root, "go-mod"),
		"runner-pip-cache": filepath.Join(root, "pip"), "runner-temp": runnerTemp, "workspace": workspace,
	}
	for name, path := range directories {
		directory := contract.Directories[name]
		directory.Path = path
		contract.Directories[name] = directory
	}
	contract.Environment["XDG_CONFIG_HOME"] = directories["runner-config"]
	contract.Environment["GH_CONFIG_DIR"] = directories["runner-gh-config"]
	contract.Environment["XDG_DATA_HOME"] = directories["runner-data"]
	contract.Environment["XDG_STATE_HOME"] = directories["runner-xdg-state"]
	contract.Environment["XDG_CACHE_HOME"] = runnerCache
	contract.Environment["NPM_CONFIG_CACHE"] = directories["runner-npm-cache"]
	contract.Environment["npm_config_store_dir"] = directories["runner-pnpm-store"]
	contract.Environment["PLAYWRIGHT_BROWSERS_PATH"] = directories["runner-playwright-cache"]
	contract.Environment["GOCACHE"] = directories["runner-go-build-cache"]
	contract.Environment["GOMODCACHE"] = directories["runner-go-mod-cache"]
	contract.Environment["PIP_CACHE_DIR"] = directories["runner-pip-cache"]
	contract.Environment["TMPDIR"] = runnerTemp
	catalogRaw, err := os.ReadFile(filepath.Join("..", "devtools", "testdata", "catalog-protocol-v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".loki-test-devtools-catalog.json"), catalogRaw, 0600); err != nil {
		t.Fatal(err)
	}
	projectConfig := filepath.Join(workspace, "devtools.toml")
	if err := os.WriteFile(projectConfig, []byte("profile = \"fixture\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("runtime candidate rules\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitInit := exec.CommandContext(t.Context(), "/usr/bin/git", "init", "-q", "--initial-branch=main")
	gitInit.Dir = workspace
	gitInit.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if output, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	responses := map[string]any{
		".loki-test-devtools-project.json": map[string]any{"schema_version": 1, "ok": true, "data": map[string]any{"item": map[string]any{"profile": "fixture", "source": "file", "config_path": projectConfig, "root": workspace}, "paths": map[string]any{"config": "/private/config", "data": "/private/data", "cache": "/private/cache"}}},
		".loki-test-devtools-current.json": map[string]any{"schema_version": 1, "ok": true, "data": map[string]any{"profile": "fixture", "revision": 1, "items": []any{}}},
		".loki-test-devtools-next.json":    map[string]any{"schema_version": 1, "ok": true, "data": map[string]any{"profile": "fixture", "revision": 1}},
	}
	for name, value := range responses {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, name), append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	devtoolsBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	o := RuntimeOptions{Socket: socket, StateDirectory: filepath.Join(root, "state"), InboxDirectory: filepath.Join(root, "inbox"), AuditPath: filepath.Join(root, "audit", "runtime.jsonl"), AgentUID: uid, SocketGID: os.Getgid(), DevtoolsBinary: devtoolsBinary, Workspace: workspace, DockerSocket: "/run/docker.sock", SnapshotDirectory: snapshotDirectory, GitHubProxy: "http://127.0.0.1:18766", GitHubBinary: "/usr/bin/false", GitHubTempDirectory: githubTemp, RunnerUID: uid, RunnerGID: uint32(os.Getgid())}
	o.verifyDevtools = func(ctx context.Context, client *devtools.Client) (devtools.Candidate, error) {
		client.Identity = nil
		return client.Verify(ctx)
	}
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = workspace
	c.AuditLog = o.AuditPath
	c.GitHubAppID = 123
	c.GitHubAPIVersion = "2026-03-10"
	c.GitHubInstallations = []config.GitHubInstallation{
		{Account: "organization", AccountType: "organization", InstallationID: 456, Repositories: []string{"repository"}},
		{Account: "person", AccountType: "user", InstallationID: 789, Repositories: []string{"personal"}},
	}
	c.GitHubTargets = []string{"organization/repository", "person/personal"}
	c.GitHubMaxResponseBytes = 4096
	c.GitHubMaxPages = 2
	c.GitHubCommandTimeoutSeconds = 1
	c.GitHubMaxInputBytes = 4096
	c.GitHubMaxOutputBytes = 4096
	generation, err := hostpolicy.Compile(c, contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		runnerState,
		runnerCache,
		runnerTemp,
		snapshotDirectory,
		contract.Environment["XDG_CONFIG_HOME"],
		contract.Environment["GH_CONFIG_DIR"],
		contract.Environment["XDG_DATA_HOME"],
		contract.Environment["XDG_STATE_HOME"],
		contract.Environment["NPM_CONFIG_CACHE"],
		contract.Environment["npm_config_store_dir"],
		contract.Environment["PLAYWRIGHT_BROWSERS_PATH"],
		contract.Environment["GOCACHE"],
		contract.Environment["GOMODCACHE"],
		contract.Environment["PIP_CACHE_DIR"],
	} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	controller := secret.Controller{StateDirectory: o.StateDirectory}
	if _, err := controller.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.ManagedCredentials().Set(t.Context(), secret.ManagedGitHubAppPrivateKey, "synthetic-platform"); err != nil {
		t.Fatal(err)
	}

	badOptions := o
	badOptions.Socket = filepath.Join(root, "bad-socket", "control.sock")
	badOptions.verifyDevtools = func(context.Context, *devtools.Client) (devtools.Candidate, error) {
		return devtools.Candidate{}, errors.New("synthetic incompatible candidate")
	}
	readyCalled := false
	if err := RunRuntime(t.Context(), badOptions, c, contract, generation, func() error { readyCalled = true; return nil }, func(err error) { t.Error(err) }); err == nil || !strings.Contains(err.Error(), "verify devtools candidate") {
		t.Fatalf("incompatible candidate error = %v", err)
	}
	if readyCalled {
		t.Fatal("runtime became ready before devtools candidate acceptance")
	}
	if _, err := os.Lstat(badOptions.Socket); !os.IsNotExist(err) {
		t.Fatalf("incompatible runtime socket exists: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(o.StateDirectory, "context")); !os.IsNotExist(err) {
		t.Fatalf("incompatible candidate created context journal state: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunRuntime(ctx, o, c, contract, generation, func() error { close(ready); return nil }, func(err error) { t.Error(err) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("readiness timeout")
	}
	client := rpc.Client{Socket: socket, ExpectedUID: &uid}
	call := func(request map[string]any) map[string]any {
		t.Helper()
		raw, err := client.Call(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err = json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	status := call(map[string]any{"operation": "status"})
	policyStatus := status["policy_generation"].(map[string]any)
	githubStatus := status["github"].(map[string]any)
	devtoolsStatus := status["devtools"].(map[string]any)
	if policyStatus["sha256"] != generation.Digest() || policyStatus["schema"] != float64(1) {
		t.Fatalf("policy generation status = %#v", policyStatus)
	}
	if status["initialized"] != true || status["profiles"] != float64(0) ||
		githubStatus["configured"] != true || githubStatus["installation_count"] != float64(2) ||
		githubStatus["target_count"] != float64(2) || githubStatus["credential_source"] != "vault" ||
		githubStatus["credential_available"] != true ||
		devtoolsStatus["version"] != "0.17.0" || devtoolsStatus["commit"] != "runtime-test" ||
		devtoolsStatus["protocol_version"] != float64(3) || devtoolsStatus["approved_commands"] != float64(len(devtools.ApprovedNames())) ||
		len(devtoolsStatus["catalog_sha256"].(string)) != 64 {
		t.Fatal(status)
	}
	if info, err := os.Stat(filepath.Join(o.StateDirectory, "context", "records")); err != nil || !info.IsDir() {
		t.Fatalf("runtime context journal directory = %v, %v", info, err)
	}
	mcpConfig := c
	mcpConfig.AuditLog = filepath.Join(root, "audit", "mcp.jsonl")
	mcpPorts, err := ProtectedPortPolicy(mcpConfig.Port, contract)
	if err != nil {
		t.Fatal(err)
	}
	mcpApp, err := NewMCP(mcpConfig, MCPOptions{
		Runtime: client, PortGuard: client,
		Browser: browserFixture(func(context.Context, string, map[string]any) (map[string]any, error) {
			return map[string]any{"status": "running"}, nil
		}),
		Jobs:  jobControllerFixture(),
		Ports: mcpPorts, Policy: generation, Token: strings.Repeat("m", 43),
		RuntimeSocket: socket,
		Environment: map[string]string{
			"HOME": t.TempDir(), "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mcpApp.Close()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpApp.Server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	mcpClient, err := mcp.NewClient(&mcp.Implementation{Name: "runtime-candidate-acceptance", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpClient.Close()
	invoke := func(name string, arguments map[string]any) map[string]any {
		t.Helper()
		result, err := mcpClient.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
		if err != nil {
			t.Fatalf("%s transport: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s failed: %#v", name, result)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	guidance := invoke("agent_guidance", map[string]any{"action": "context", "cwd": ".", "target": "."})
	guidanceValue := guidance["guidance"].(map[string]any)
	sources := guidanceValue["sources"].([]any)
	if len(sources) != 1 || sources[0].(map[string]any)["content"] != "runtime candidate rules\n" {
		t.Fatalf("native guidance = %#v", guidance)
	}
	current := invoke("project_coordination", map[string]any{"action": "current", "cwd": "."})
	if current["profile"] != "fixture" || current["revision"] != float64(1) {
		t.Fatalf("baseline project coordination = %#v", current)
	}
	resume := invoke("project_context", map[string]any{"cwd": ".", "target": "."})
	if resume["transition"].(map[string]any)["action"] != "none" ||
		resume["checkpoint"].(map[string]any)["found"] != false {
		t.Fatalf("baseline project context = %#v", resume)
	}
	resumeBasis := resume["basis"].(map[string]any)
	if len(resumeBasis["repository_id"].(string)) != 64 || len(resumeBasis["worktree_id"].(string)) != 64 {
		t.Fatalf("baseline project context basis = %#v", resumeBasis)
	}
	draft := serviceContextDraft()
	put := call(map[string]any{
		"operation":         "context_checkpoint_put",
		"request_id":        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"expected_previous": "missing",
		"draft":             draft,
	})
	record := put["record"].(map[string]any)
	if put["replayed"] != false || len(record["id"].(string)) != 64 {
		t.Fatalf("context put = %#v", put)
	}
	latest := call(map[string]any{
		"operation":     "context_checkpoint_latest",
		"repository_id": draft.Basis.RepositoryID,
		"worktree_id":   draft.Basis.WorktreeID,
		"workstream_id": draft.Basis.WorkstreamID,
	})
	latestRecord := latest["record"].(map[string]any)
	if latest["found"] != true || latestRecord["id"] != record["id"] {
		t.Fatalf("context latest = %#v", latest)
	}
	for _, protected := range []int{18765, 18766, 18767} {
		if _, err := client.Call(t.Context(), map[string]any{"operation": "inspect", "port": protected}); err == nil {
			t.Fatalf("runtime accepted protected port %d", protected)
		}
		if _, err := client.Call(t.Context(), map[string]any{"operation": "inspect_docker_port", "port": protected}); err == nil {
			t.Fatalf("runtime Docker inspector accepted protected port %d", protected)
		}
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	ports := call(map[string]any{"operation": "inspect", "port": port})
	if ports["in_use"] != false {
		t.Fatal(ports)
	}
	beforeProfiles := call(map[string]any{"operation": "list_profiles"})
	call(map[string]any{
		"operation": "profile_create_request", "profile": "fixture",
		"expected_revision": beforeProfiles["revision"],
		"request_id":        "85000000-0000-4000-8000-000000000001",
	})
	profiles := call(map[string]any{"operation": "list_profiles"})
	if len(profiles["profiles"].([]any)) != 1 {
		t.Fatal(profiles)
	}
	log := call(map[string]any{"operation": "audit"})
	if len(log["records"].([]any)) < 3 {
		t.Fatal(log)
	}
	if _, err := client.Call(t.Context(), map[string]any{"operation": "devtools_call", "command": "run", "input": map[string]any{}}); err == nil {
		t.Fatal("runtime did not install the devtools broker operation")
	}
	if _, err := client.Call(t.Context(), map[string]any{"operation": "github_command", "target": "organization/repository", "args": []string{"issue", "list"}}); err == nil {
		t.Fatal("runtime did not install the GitHub command broker")
	}
	if _, err := client.Call(t.Context(), map[string]any{"operation": "github_fields_list", "target": "person/personal"}); err == nil {
		t.Fatal("personal repository accepted for organization-only Issue Fields")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatal("runtime socket retained", err)
	}
}
