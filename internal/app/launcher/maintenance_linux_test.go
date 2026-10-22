//go:build linux

package launcher

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"loki/internal/rpc"
	"loki/modules/execution/jobs/remote"
)

func TestHostMaintenanceKernelUIDBoundary(t *testing.T) {
	if os.Getenv("LOKI_MAINTENANCE_PEER_TEST") == "1" {
		root := uint32(0)
		_, err := (rpc.Client{Socket: os.Getenv("LOKI_MAINTENANCE_SOCKET"), ExpectedUID: &root}).Call(t.Context(), map[string]any{"operation": "maintenance_begin", "operation_id": "11111111-1111-4111-8111-111111111111", "interrupt": true})
		if err == nil || !strings.Contains(err.Error(), "host administrator") {
			t.Fatalf("non-root peer gained host mutation: %v", err)
		}
		return
	}
	if os.Geteuid() != 0 {
		if os.Getenv("LOKI_REQUIRE_HOST_MAINTENANCE") == "1" {
			t.Fatal("required root UID fixture is unavailable")
		}
		t.Skip("requires isolated root UID fixture")
	}
	outer, err := os.MkdirTemp("/tmp", "loki-host-maintenance-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outer)
	if err := os.Chmod(outer, 0755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(outer, "launcher")
	if err := os.Mkdir(parent, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(parent, 0, 1001); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(parent, "control.sock")
	journal := launcherJournal(t, time.Minute, 8)
	runner := &fakeRunner{blockObserve: true, observing: make(chan struct{}), canceled: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- Run(ctx, Options{Socket: socket, SocketGID: 1001, ExecutorUID: 1001, Policy: launcherPolicy(t), Runner: runner, Journal: journal, RunTimeout: 5 * time.Second, Ready: func() error { close(ready); return nil }})
	}()
	select {
	case <-ready:
	case err := <-finished:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("launcher did not become ready")
	}
	client, err := remote.NewHostMaintenance(socket)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := client.Status(t.Context()); err != nil || state != nil {
		t.Fatalf("initial state: %+v %v", state, err)
	}
	// Traverse the same real socket as the executor UID, bypassing CLI's root
	// check to verify that SO_PEERCRED and the service grant enforce the rule.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	copyPath := filepath.Join(outer, "peer-tests")
	output, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0555)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy peer executable: %v %v", copyErr, closeErr)
	}
	peer := exec.Command(copyPath, "-test.run=^TestHostMaintenanceKernelUIDBoundary$", "-test.timeout=10s")
	peer.Env = []string{"LOKI_MAINTENANCE_PEER_TEST=1", "LOKI_MAINTENANCE_SOCKET=" + socket}
	peer.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1001, Gid: 1001}}
	if raw, err := peer.CombinedOutput(); err != nil {
		t.Fatalf("executor boundary: %v %s", err, raw)
	}
	root := uint32(0)
	rpcClient := rpc.Client{Socket: socket, ExpectedUID: &root}
	jobID := strings.Repeat("a", 32)
	if _, err := rpcClient.Call(t.Context(), map[string]any{"operation": "start", "id": jobID, "cwd": ".", "argv": []string{"/bin/true"}, "policy_sha256": strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.observing:
	case <-time.After(time.Second):
		t.Fatal("Job did not start")
	}
	op := "11111111-1111-4111-8111-111111111111"
	if _, err := client.Begin(t.Context(), op, false); err == nil {
		t.Fatal("default maintenance interrupted a live Job")
	}
	if state, err := client.Begin(t.Context(), op, true); err != nil || !state.Active {
		t.Fatalf("drain: %+v %v", state, err)
	}
	raw, err := rpcClient.Call(t.Context(), map[string]any{"operation": "inspect", "id": jobID})
	var status struct {
		State   string `json:"state"`
		Cleanup string `json:"cleanup"`
	}
	if err != nil || json.Unmarshal(raw, &status) != nil || status.State != "terminal" || status.Cleanup != "complete" {
		t.Fatalf("drain cleanup: %s %v", raw, err)
	}
	if state, err := client.Status(t.Context()); err != nil || state == nil || !state.Active || state.OperationID != op {
		t.Fatalf("status: %+v %v", state, err)
	}
	if _, err := client.End(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("launcher did not stop")
	}
}
