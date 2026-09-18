package launcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/sandbox"
	"loki/internal/rpc"
)

type fakeRunner struct {
	calls    atomic.Int32
	result   sandbox.Result
	err      error
	wait     bool
	started  chan struct{}
	canceled chan struct{}
}

func (r *fakeRunner) Run(ctx context.Context, _ sandbox.Plan) (sandbox.Result, error) {
	r.calls.Add(1)
	if r.started != nil {
		close(r.started)
	}
	if r.wait {
		<-ctx.Done()
		if r.canceled != nil {
			close(r.canceled)
		}
		return sandbox.Result{}, ctx.Err()
	}
	return r.result, r.err
}

func launcherPolicy(t *testing.T) sandbox.Policy {
	t.Helper()
	policy, err := sandbox.NewPolicy(sandbox.PolicyOptions{
		GenerationSHA256: strings.Repeat("a", 64),
		Image:            "registry.example/loki@sha256:" + strings.Repeat("b", 64),
		Workspace:        t.TempDir(),
		UID:              2001,
		GID:              2001,
		Environment:      []string{"PATH=/usr/bin:/bin"},
		MemoryBytes:      256 << 20,
		PIDs:             64,
		TmpfsBytes:       32 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func launcherRequest() map[string]any {
	return map[string]any{
		"operation":     "run",
		"id":            strings.Repeat("c", 32),
		"policy_sha256": strings.Repeat("a", 64),
		"cwd":           ".",
		"argv":          []string{"/bin/true"},
	}
}

func TestOperationsExposeOnlyExecutorWorkloadRun(t *testing.T) {
	runner := &fakeRunner{}
	operations, err := Operations(launcherPolicy(t), runner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("operations = %#v", operations)
	}
	operation, ok := operations["run"]
	if !ok || operation.Grant != controlpolicy.WorkloadLaunch || operation.Timeout != time.Second {
		t.Fatalf("run operation = %#v", operation)
	}
	server := rpc.Server{Principals: identity.FixedUIDResolver{UID: 1001, Kind: identity.Executor}}
	if !server.Authorized(rpc.Peer{UID: 1001}, controlpolicy.WorkloadLaunch) {
		t.Fatal("executor was denied workload launch")
	}
	if !server.Authorized(rpc.Peer{UID: 0}, controlpolicy.WorkloadLaunch) {
		t.Fatal("host administrator was denied workload launch")
	}
	if server.Authorized(rpc.Peer{UID: 1002}, controlpolicy.WorkloadLaunch) {
		t.Fatal("unknown peer obtained workload launch")
	}
	if server.Authorized(rpc.Peer{UID: 1001}, controlpolicy.Agent) {
		t.Fatal("executor obtained agent grant")
	}
}

func TestRunOperationRejectsInvalidRequestsBeforeRunner(t *testing.T) {
	runner := &fakeRunner{}
	operations, err := Operations(launcherPolicy(t), runner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	operation := operations["run"]

	withExtra := launcherRequest()
	withExtra["extra"] = true
	raw, _ := json.Marshal(withExtra)
	if _, err := operation.Handle(t.Context(), raw); err == nil {
		t.Fatal("unknown field was accepted")
	}

	wrongPolicy := launcherRequest()
	wrongPolicy["policy_sha256"] = strings.Repeat("d", 64)
	raw, _ = json.Marshal(wrongPolicy)
	if _, err := operation.Handle(t.Context(), raw); err == nil {
		t.Fatal("wrong policy generation was accepted")
	}

	if got := runner.calls.Load(); got != 0 {
		t.Fatalf("runner calls = %d before valid workload", got)
	}
}

func TestRunOperationReturnsOnlyExitCode(t *testing.T) {
	runner := &fakeRunner{result: sandbox.Result{ExitCode: 7}}
	operations, err := Operations(launcherPolicy(t), runner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(launcherRequest())
	result, err := operations["run"].Handle(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"exit_code":7}` {
		t.Fatalf("result = %s", encoded)
	}
}

func TestRunRequiresValidatedInputs(t *testing.T) {
	if _, err := Operations(sandbox.Policy{}, &fakeRunner{}, time.Second); err == nil {
		t.Fatal("zero sandbox policy was accepted")
	}
	if _, err := Operations(launcherPolicy(t), nil, time.Second); err == nil {
		t.Fatal("nil runner was accepted")
	}
	if _, err := Operations(launcherPolicy(t), &fakeRunner{}, 0); err == nil {
		t.Fatal("zero timeout was accepted")
	}
	if err := Run(t.Context(), Options{
		Socket:      filepath.Join(t.TempDir(), "launcher.sock"),
		SocketGID:   os.Getgid(),
		ExecutorUID: 0,
		Policy:      launcherPolicy(t),
		Runner:      &fakeRunner{},
		RunTimeout:  time.Second,
	}); err == nil {
		t.Fatal("root executor UID was accepted")
	}
}

func TestRunTimeoutCancelsRunnerThroughRPC(t *testing.T) {
	currentUID := uint32(os.Getuid())
	executorUID := currentUID
	if executorUID == 0 {
		executorUID = 1001
	}
	socket := filepath.Join(t.TempDir(), "launcher", "control.sock")
	runner := &fakeRunner{wait: true, started: make(chan struct{}), canceled: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Socket:      socket,
			SocketGID:   os.Getgid(),
			ExecutorUID: executorUID,
			Policy:      launcherPolicy(t),
			Runner:      runner,
			RunTimeout:  time.Second,
			Ready: func() error {
				close(ready)
				return nil
			},
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("launcher readiness timeout")
	}

	client := rpc.Client{Socket: socket, Limits: rpc.Limits{Timeout: 3 * time.Second}}
	_, err := client.Call(t.Context(), launcherRequest())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	select {
	case <-runner.started:
	default:
		t.Fatal("runner was not invoked")
	}
	select {
	case <-runner.canceled:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe cancellation")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("launcher shutdown timeout")
	}
}
