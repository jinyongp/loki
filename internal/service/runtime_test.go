package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loki/internal/action"
	"loki/internal/config"
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
	o := RuntimeOptions{Socket: socket, StateDirectory: filepath.Join(root, "state"), InboxDirectory: filepath.Join(root, "inbox"), ProjectStateDirectory: filepath.Join(root, "projects"), AuditPath: filepath.Join(root, "audit", "runtime.jsonl"), AgentUID: uid, SocketGID: os.Getgid(), TaskHome: filepath.Join(root, "task-home"), TaskBinary: "/home/linuxbrew/.linuxbrew/bin/task", Layout: action.Layout{Workspace: workspace, Binary: "/unneeded-for-read-only-fixture", RuntimeSocket: socket, RuntimeUID: uid, UID: uid, GID: uint32(os.Getgid()), CallbackPort: new(int)}}
	controller := secret.Controller{StateDirectory: o.StateDirectory}
	if _, err := controller.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunRuntime(ctx, c, o, func() error { close(ready); return nil }, func(err error) { t.Error(err) })
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
	if status["initialized"] != true || status["running_processes"] != float64(0) {
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
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
	if _, err = os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatal("runtime socket retained", err)
	}
}
