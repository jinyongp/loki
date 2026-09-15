package service

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loki/internal/config"
	"loki/internal/execution"
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
	contractPath := filepath.Join(root, "execution-contract.json")
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
	encodedContract, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(contractPath, encodedContract, 0600); err != nil {
		t.Fatal(err)
	}
	o := RuntimeOptions{Socket: socket, StateDirectory: filepath.Join(root, "state"), InboxDirectory: filepath.Join(root, "inbox"), AuditPath: filepath.Join(root, "audit", "runtime.jsonl"), AgentUID: uid, SocketGID: os.Getgid(), DevtoolsBinary: "/usr/bin/false", ExecutionContract: contractPath, Workspace: workspace, DockerSocket: "/run/docker.sock", SnapshotDirectory: snapshotDirectory, GitHubProxy: "http://127.0.0.1:18766", GitHubBinary: "/usr/bin/false", RunnerUID: uid, RunnerGID: uint32(os.Getgid())}
	c := config.Config{
		GitHubAppID: 123, GitHubAPIVersion: "2026-03-10",
		GitHubInstallations: []config.GitHubInstallation{
			{Account: "organization", AccountType: "organization", InstallationID: 456, Repositories: []string{"repository"}},
			{Account: "person", AccountType: "user", InstallationID: 789, Repositories: []string{"personal"}},
		},
		GitHubTargets:          []string{"organization/repository", "person/personal"},
		GitHubMaxResponseBytes: 4096, GitHubMaxPages: 2,
		GitHubCommandTimeoutSeconds: 1, GitHubMaxInputBytes: 4096, GitHubMaxOutputBytes: 4096,
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
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunRuntime(ctx, o, c, func() error { close(ready); return nil }, func(err error) { t.Error(err) })
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
	if status["initialized"] != true || status["profiles"] != float64(0) {
		t.Fatal(status)
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
	call(map[string]any{"operation": "profile_create", "profile": "fixture"})
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
