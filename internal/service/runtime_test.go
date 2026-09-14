package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	for name, path := range map[string]string{"runner-state": runnerState, "runner-cache": runnerCache, "runner-temp": runnerTemp, "workspace": workspace} {
		directory := contract.Directories[name]
		directory.Path = path
		contract.Directories[name] = directory
	}
	contract.Environment["XDG_CONFIG_HOME"] = filepath.Join(runnerState, "config")
	contract.Environment["GH_CONFIG_DIR"] = filepath.Join(runnerState, "config", "gh")
	contract.Environment["XDG_DATA_HOME"] = filepath.Join(runnerState, "data")
	contract.Environment["XDG_STATE_HOME"] = filepath.Join(runnerState, "state")
	contract.Environment["XDG_CACHE_HOME"] = runnerCache
	contract.Environment["NPM_CONFIG_CACHE"] = filepath.Join(runnerCache, "npm")
	contract.Environment["npm_config_store_dir"] = filepath.Join(runnerCache, "pnpm")
	contract.Environment["PLAYWRIGHT_BROWSERS_PATH"] = filepath.Join(runnerCache, "playwright")
	contract.Environment["GOCACHE"] = filepath.Join(runnerCache, "go-build")
	contract.Environment["GOMODCACHE"] = filepath.Join(runnerCache, "go-mod")
	contract.Environment["PIP_CACHE_DIR"] = filepath.Join(runnerCache, "pip")
	contract.Environment["TMPDIR"] = runnerTemp
	encodedContract, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(contractPath, encodedContract, 0600); err != nil {
		t.Fatal(err)
	}
	snapshotDirectory := filepath.Join(runnerState, "snapshots")
	o := RuntimeOptions{Socket: socket, StateDirectory: filepath.Join(root, "state"), InboxDirectory: filepath.Join(root, "inbox"), AuditPath: filepath.Join(root, "audit", "runtime.jsonl"), AgentUID: uid, SocketGID: os.Getgid(), DevtoolsBinary: "/usr/bin/false", ExecutionContract: contractPath, Workspace: workspace, DockerSocket: "/run/docker.sock", SnapshotDirectory: snapshotDirectory, RunnerUID: uid, RunnerGID: uint32(os.Getgid())}
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
		done <- RunRuntime(ctx, o, func() error { close(ready); return nil }, func(err error) { t.Error(err) })
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
