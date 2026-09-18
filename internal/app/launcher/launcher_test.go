package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/sandbox"
	"loki/internal/rpc"
)

type fakeRunner struct {
	mu        sync.Mutex
	calls     int
	result    sandbox.Result
	err       error
	wait      bool
	started   chan struct{}
	canceled  chan struct{}
	startOne  sync.Once
	cancelOne sync.Once
}

func (r *fakeRunner) Run(ctx context.Context, _ sandbox.Plan) (sandbox.Result, error) {
	r.mu.Lock()
	r.calls++
	wait, result, err := r.wait, r.result, r.err
	r.mu.Unlock()
	if r.started != nil {
		r.startOne.Do(func() { close(r.started) })
	}
	if wait {
		<-ctx.Done()
		if r.canceled != nil {
			r.cancelOne.Do(func() { close(r.canceled) })
		}
		return sandbox.Result{}, ctx.Err()
	}
	return result, err
}

func (r *fakeRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
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

func launcherSpec(id string) sandbox.WorkloadSpec {
	return sandbox.WorkloadSpec{
		ID:           id,
		PolicySHA256: strings.Repeat("a", 64),
		CWD:          ".",
		Argv:         []string{"/bin/true"},
	}
}

func lifecycleFixture(t *testing.T, runner Runner, timeout, retention time.Duration, maxJobs int) *lifecycle {
	t.Helper()
	l, err := newLifecycle(t.Context(), launcherPolicy(t), runner, timeout, retention, maxJobs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.close(); err != nil {
			t.Errorf("close lifecycle: %v", err)
		}
	})
	return l
}

func encodedRequest(t *testing.T, value map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLifecycleOperationsExposeOnlyExecutorJobLifecycle(t *testing.T) {
	l := lifecycleFixture(t, &fakeRunner{}, time.Second, time.Second, 8)
	operations := l.operations()
	if len(operations) != 3 {
		t.Fatalf("operations = %#v", operations)
	}
	for _, name := range []string{"start", "wait", "cancel"} {
		operation, ok := operations[name]
		if !ok || operation.Grant != controlpolicy.WorkloadLaunch {
			t.Fatalf("%s operation = %#v", name, operation)
		}
	}
	if operations["wait"].Timeout != time.Second {
		t.Fatalf("wait timeout = %v", operations["wait"].Timeout)
	}
	server := rpc.Server{Principals: identity.FixedUIDResolver{UID: 1001, Kind: identity.Executor}}
	if !server.Authorized(rpc.Peer{UID: 1001}, controlpolicy.WorkloadLaunch) {
		t.Fatal("executor was denied workload launch")
	}
	if server.Authorized(rpc.Peer{UID: 1001}, controlpolicy.Agent) {
		t.Fatal("executor obtained agent grant")
	}
}

func TestStartRejectsInvalidRequestsBeforeRunner(t *testing.T) {
	runner := &fakeRunner{}
	l := lifecycleFixture(t, runner, time.Second, time.Second, 8)
	operation := l.operations()["start"]

	withExtra := map[string]any{
		"operation":     "start",
		"id":            strings.Repeat("c", 32),
		"policy_sha256": strings.Repeat("a", 64),
		"cwd":           ".",
		"argv":          []string{"/bin/true"},
		"extra":         true,
	}
	if _, err := operation.Handle(t.Context(), encodedRequest(t, withExtra)); err == nil {
		t.Fatal("unknown field was accepted")
	}

	wrongPolicy := launcherSpec(strings.Repeat("d", 32))
	wrongPolicy.PolicySHA256 = strings.Repeat("e", 64)
	if err := l.start(wrongPolicy); err == nil {
		t.Fatal("wrong policy generation was accepted")
	}
	if runner.callCount() != 0 {
		t.Fatalf("runner calls = %d", runner.callCount())
	}
}

func TestStartWaitReturnsExitCodeAndConsumesResult(t *testing.T) {
	id := strings.Repeat("c", 32)
	l := lifecycleFixture(t, &fakeRunner{result: sandbox.Result{ExitCode: 7}}, time.Second, time.Second, 8)
	if err := l.start(launcherSpec(id)); err != nil {
		t.Fatal(err)
	}
	result, err := l.wait(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
	if _, err = l.wait(t.Context(), id); err == nil {
		t.Fatal("consumed job remained registered")
	}
}

func TestWaitTimeoutDoesNotCancelJobButExplicitCancelDoes(t *testing.T) {
	id := strings.Repeat("c", 32)
	runner := &fakeRunner{wait: true, started: make(chan struct{}), canceled: make(chan struct{})}
	l := lifecycleFixture(t, runner, 5*time.Second, time.Second, 8)
	if err := l.start(launcherSpec(id)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}

	waitCtx, waitCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer waitCancel()
	if _, err := l.wait(waitCtx, id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v", err)
	}
	select {
	case <-runner.canceled:
		t.Fatal("wait timeout canceled the workload")
	default:
	}

	cancelCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	canceled, err := l.cancelJob(cancelCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !canceled {
		t.Fatal("active job was not canceled")
	}
	select {
	case <-runner.canceled:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe cancellation")
	}
}

func TestDuplicateCapacityAndRetentionAreBounded(t *testing.T) {
	first := strings.Repeat("c", 32)
	second := strings.Repeat("d", 32)
	runner := &fakeRunner{wait: true}
	l := lifecycleFixture(t, runner, 5*time.Second, 30*time.Millisecond, 1)
	if err := l.start(launcherSpec(first)); err != nil {
		t.Fatal(err)
	}
	if err := l.start(launcherSpec(first)); err == nil {
		t.Fatal("duplicate ID was accepted")
	}
	if err := l.start(launcherSpec(second)); err == nil {
		t.Fatal("capacity overflow was accepted")
	}
	cancelCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	if _, err := l.cancelJob(cancelCtx, first); err != nil {
		t.Fatal(err)
	}
	cancel()

	quick := lifecycleFixture(t, &fakeRunner{result: sandbox.Result{ExitCode: 0}}, time.Second, 20*time.Millisecond, 1)
	if err := quick.start(launcherSpec(second)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := quick.wait(t.Context(), second); err == nil {
		t.Fatal("expired completed job remained registered")
	}
	if err := quick.start(launcherSpec(first)); err != nil {
		t.Fatalf("expired result did not release capacity: %v", err)
	}
}

func TestCloseCancelsOwnedJobs(t *testing.T) {
	id := strings.Repeat("c", 32)
	runner := &fakeRunner{wait: true, started: make(chan struct{}), canceled: make(chan struct{})}
	l, err := newLifecycle(t.Context(), launcherPolicy(t), runner, 5*time.Second, time.Second, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.start(launcherSpec(id)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	if err = l.close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel runner")
	}
	if err = l.start(launcherSpec(strings.Repeat("d", 32))); err == nil {
		t.Fatal("closed lifecycle accepted a new job")
	}
}

func TestCancellationErrorsDoNotHideCleanupFailures(t *testing.T) {
	if !cancellationOnly(context.Canceled) || !cancellationOnly(errors.Join(context.Canceled, context.DeadlineExceeded)) {
		t.Fatal("pure cancellation was not recognized")
	}
	if cancellationOnly(errors.Join(context.Canceled, errors.New("cleanup failed"))) {
		t.Fatal("cleanup failure was hidden as cancellation")
	}
}

func TestLifecycleRequiresValidatedInputs(t *testing.T) {
	runner := &fakeRunner{}
	if _, err := newLifecycle(t.Context(), sandbox.Policy{}, runner, time.Second, time.Second, 8); err == nil {
		t.Fatal("zero sandbox policy was accepted")
	}
	if _, err := newLifecycle(t.Context(), launcherPolicy(t), nil, time.Second, time.Second, 8); err == nil {
		t.Fatal("nil runner was accepted")
	}
	if _, err := newLifecycle(t.Context(), launcherPolicy(t), runner, 0, time.Second, 8); err == nil {
		t.Fatal("zero timeout was accepted")
	}
	if _, err := newLifecycle(t.Context(), launcherPolicy(t), runner, time.Second, 0, 8); err == nil {
		t.Fatal("zero retention was accepted")
	}
	if _, err := newLifecycle(t.Context(), launcherPolicy(t), runner, time.Second, time.Second, 0); err == nil {
		t.Fatal("zero job capacity was accepted")
	}
	if err := Run(t.Context(), Options{
		Socket:          filepath.Join(t.TempDir(), "launcher.sock"),
		SocketGID:       os.Getgid(),
		ExecutorUID:     0,
		Policy:          launcherPolicy(t),
		Runner:          runner,
		RunTimeout:      time.Second,
		ResultRetention: time.Second,
		MaxJobs:         8,
	}); err == nil {
		t.Fatal("root executor UID was accepted")
	}
}
