package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	applauncher "loki/internal/app/launcher"
	"loki/internal/platform/sandbox"
	"loki/internal/rpc"
	"loki/internal/work/jobs"
	"loki/internal/work/jobs/remote"
)

type recordingSandboxRunner struct {
	mu           sync.Mutex
	plans        []sandbox.Plan
	startErr     error
	wait         bool
	waitStarted  chan struct{}
	waitCanceled chan struct{}
	waitOnce     sync.Once
	cancelOnce   sync.Once
}

func recordingInstanceRef() string {
	return "oci-instance-sha256:" + strings.Repeat("2", 64)
}

func (r *recordingSandboxRunner) StartJob(_ context.Context, plan sandbox.Plan) (sandbox.StartResult, error) {
	r.mu.Lock()
	r.plans = append(r.plans, plan)
	err := r.startErr
	r.mu.Unlock()
	result := sandbox.StartResult{Resource: plan.Resource()}
	if err == nil {
		result.Created = true
		result.Started = true
		result.InstanceRef = recordingInstanceRef()
	}
	return result, err
}

func (r *recordingSandboxRunner) EndpointBindings(
	_ context.Context, _ sandbox.Resource, _ string, _ []sandbox.EndpointSpec,
) ([]sandbox.EndpointBinding, error) {
	return nil, nil
}

func (r *recordingSandboxRunner) OutputJob(ctx context.Context, resource sandbox.Resource, instanceRef string) ([]byte, bool, error) {
	return r.OutputJobLimit(ctx, resource, instanceRef, jobs.MaxOutputBytes)
}

func (r *recordingSandboxRunner) OutputJobLimit(_ context.Context, _ sandbox.Resource, _ string, maximum int) ([]byte, bool, error) {
	output := []byte("executor-output")
	if len(output) > maximum {
		return append([]byte(nil), output[:maximum]...), true, nil
	}
	return output, false, nil
}

func (r *recordingSandboxRunner) ObserveJob(ctx context.Context, _ sandbox.Resource, _ string) (sandbox.Result, error) {
	r.mu.Lock()
	wait := r.wait
	started, canceled := r.waitStarted, r.waitCanceled
	r.mu.Unlock()
	if wait {
		if started != nil {
			r.waitOnce.Do(func() { close(started) })
		}
		<-ctx.Done()
		if canceled != nil {
			r.cancelOnce.Do(func() { close(canceled) })
		}
		outcome := sandbox.OutcomeCanceled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			outcome = sandbox.OutcomeTimedOut
		}
		return sandbox.Result{Outcome: outcome, Cleanup: sandbox.CleanupPending}, nil
	}
	return sandbox.Result{
		ExitCode: 17, ExitCodeKnown: true, Outcome: sandbox.OutcomeExited,
		Output: []byte("executor-output"), Cleanup: sandbox.CleanupPending,
	}, nil
}

func (r *recordingSandboxRunner) CleanupJob(_ context.Context, _ sandbox.Resource, _ string) (sandbox.CleanupStatus, error) {
	return sandbox.CleanupComplete, nil
}

func (r *recordingSandboxRunner) Inspect(_ context.Context, _ sandbox.Resource) (sandbox.ResourceState, error) {
	return sandbox.ResourceState{}, nil
}

func (r *recordingSandboxRunner) InspectJob(_ context.Context, _ sandbox.Resource, _ string) (sandbox.ResourceState, error) {
	return sandbox.ResourceState{}, nil
}

func (r *recordingSandboxRunner) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.plans)
}

func (r *recordingSandboxRunner) plan(index int) sandbox.Plan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.plans[index]
}

func (r *recordingSandboxRunner) setFailure(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startErr, r.wait = err, false
}

func (r *recordingSandboxRunner) setWait() (<-chan struct{}, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startErr, r.wait = nil, true
	r.waitStarted = make(chan struct{})
	r.waitCanceled = make(chan struct{})
	r.waitOnce = sync.Once{}
	r.cancelOnce = sync.Once{}
	return r.waitStarted, r.waitCanceled
}

func waitReady(t *testing.T, ready <-chan struct{}, done <-chan error, role string) {
	t.Helper()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("%s failed before readiness: %v", role, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("%s readiness timeout", role)
	}
}

func TestExecutorToLauncherTrustedAuthorityBoundary(t *testing.T) {
	const policyDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	roleUID := uint32(os.Getuid())
	if roleUID == 0 {
		roleUID = 1001
	}
	actualUID := uint32(os.Getuid())

	policy, err := sandbox.NewPolicy(sandbox.PolicyOptions{
		GenerationSHA256: policyDigest,
		Image:            "registry.example/loki@sha256:" + strings.Repeat("b", 64),
		Gateway: sandbox.GatewayPolicyOptions{
			Image:  "registry.example/loki-gateway@sha256:" + strings.Repeat("c", 64),
			Binary: "/opt/loki/bin/loki", ExecutionContract: "/usr/share/doc/loki/execution-contract.json",
			EgressPolicy: "/usr/share/doc/loki/egress-policy.json", ProxyPort: 18766,
			MemoryBytes: 128 << 20, PIDs: 32, TmpfsBytes: 16 << 20,
		},
		Workspace:   t.TempDir(),
		UID:         2001,
		GID:         2001,
		Environment: []string{"PATH=/usr/bin:/bin"},
		MemoryBytes: 256 << 20,
		PIDs:        64,
		TmpfsBytes:  32 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	launcherSocket := filepath.Join(root, "launcher", "control.sock")
	executorSocket := filepath.Join(root, "executor", "control.sock")
	journalDir := filepath.Join(root, "jobs")
	if err = os.Mkdir(journalDir, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := jobs.OpenJournal(journalDir, jobs.JournalLimits{
		MaxRecords: 8, MaxRecordBytes: 128 << 10, MaxOutputBytes: 4096, Retention: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	runner := &recordingSandboxRunner{}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	launcherReady := make(chan struct{})
	launcherDone := make(chan error, 1)
	go func() {
		launcherDone <- applauncher.Run(ctx, applauncher.Options{
			Socket:      launcherSocket,
			SocketGID:   os.Getgid(),
			ExecutorUID: roleUID,
			Policy:      policy,
			Runner:      runner,
			Journal:     journal,
			RunTimeout:  10 * time.Second,
			Ready: func() error {
				close(launcherReady)
				return nil
			},
		})
	}()
	waitReady(t, launcherReady, launcherDone, "launcher")

	launcherClient, err := remote.New(remote.Options{
		Socket:       launcherSocket,
		ExpectedUID:  &actualUID,
		PolicySHA256: policyDigest,
		Timeout:      8 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewService(launcherClient, jobs.RandomID)
	if err != nil {
		t.Fatal(err)
	}

	executorReady := make(chan struct{})
	executorDone := make(chan error, 1)
	go func() {
		executorDone <- Run(ctx, Options{
			Socket:     executorSocket,
			SocketGID:  os.Getgid(),
			AgentUID:   roleUID,
			Runner:     jobService,
			RunTimeout: 6 * time.Second,
			Ready: func() error {
				close(executorReady)
				return nil
			},
		})
	}()
	waitReady(t, executorReady, executorDone, "executor")

	client := rpc.Client{
		Socket:      executorSocket,
		ExpectedUID: &actualUID,
		Limits:      rpc.Limits{Timeout: 5 * time.Second},
	}

	for _, forged := range []map[string]any{
		{"operation": "run", "cwd": ".", "argv": []string{"/bin/true"}, "id": strings.Repeat("c", 32)},
		{"operation": "run", "cwd": ".", "argv": []string{"/bin/true"}, "policy_sha256": strings.Repeat("d", 64)},
		{"operation": "run", "cwd": ".", "argv": []string{"/bin/true"}, "extra": true},
		{"operation": "start", "request_id": "123e4567-e89b-12d3-a456-426614174099", "cwd": ".", "argv": []string{"/bin/true"}, "job_id": strings.Repeat("c", 32)},
		{"operation": "start", "request_id": "123e4567-e89b-12d3-a456-426614174099", "cwd": ".", "argv": []string{"/bin/true"}, "policy_sha256": strings.Repeat("d", 64)},
	} {
		if _, err := client.Call(t.Context(), forged); err == nil {
			t.Fatalf("forged executor request was accepted: %#v", forged)
		}
	}
	if runner.calls() != 0 {
		t.Fatalf("runner calls after forged requests = %d", runner.calls())
	}

	raw, err := client.Call(t.Context(), map[string]any{
		"operation": "run",
		"cwd":       ".",
		"argv":      []string{"/bin/true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	jobID, _ := result["job_id"].(string)
	exitCode, _ := result["exit_code"].(float64)
	if len(result) != 6 || !regexp.MustCompile("^[0-9a-f]{32}$").MatchString(jobID) || exitCode != 17 ||
		result["outcome"] != string(jobs.OutcomeExited) || result["output"] != "ZXhlY3V0b3Itb3V0cHV0" ||
		result["truncated"] != false || result["cleanup"] != string(jobs.CleanupComplete) {
		t.Fatalf("executor result = %#v", result)
	}
	if runner.calls() != 1 {
		t.Fatalf("runner calls = %d", runner.calls())
	}
	plan := runner.plan(0)
	if plan.Name() != "loki-job-"+jobID || plan.PolicySHA256() != policyDigest {
		t.Fatalf("launcher plan name=%q policy=%q", plan.Name(), plan.PolicySHA256())
	}

	asyncEntered, asyncCanceled := runner.setWait()
	asyncRequestID := "123e4567-e89b-12d3-a456-426614174100"
	raw, err = client.Call(t.Context(), map[string]any{
		"operation": "start", "request_id": asyncRequestID,
		"cwd": ".", "argv": []string{"/bin/sleep", "10"}, "timeout_seconds": 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	var asyncStart jobs.StartResult
	if err = json.Unmarshal(raw, &asyncStart); err != nil {
		t.Fatal(err)
	}
	if asyncStart.RequestID != asyncRequestID || !asyncStart.Detached || asyncStart.Replayed ||
		!regexp.MustCompile("^[0-9a-f]{32}$").MatchString(asyncStart.JobID) {
		t.Fatalf("async start = %#v", asyncStart)
	}
	select {
	case <-asyncEntered:
	case <-time.After(time.Second):
		t.Fatal("async start did not reach sandbox runner")
	}

	raw, err = client.Call(t.Context(), map[string]any{"operation": "inspect", "job_id": asyncStart.JobID})
	if err != nil {
		t.Fatal(err)
	}
	var asyncStatus jobs.Status
	if err = json.Unmarshal(raw, &asyncStatus); err != nil {
		t.Fatal(err)
	}
	if asyncStatus.JobID != asyncStart.JobID || asyncStatus.State != jobs.StateRunning {
		t.Fatalf("async inspect = %#v", asyncStatus)
	}

	raw, err = client.Call(t.Context(), map[string]any{"operation": "output", "job_id": asyncStart.JobID})
	if err != nil {
		t.Fatal(err)
	}
	var asyncOutput jobs.OutputSnapshot
	if err = json.Unmarshal(raw, &asyncOutput); err != nil {
		t.Fatal(err)
	}
	if asyncOutput.JobID != asyncStart.JobID || asyncOutput.State != jobs.StateRunning ||
		asyncOutput.Output != "executor-output" || asyncOutput.Complete {
		t.Fatalf("async output = %#v", asyncOutput)
	}

	raw, err = client.Call(t.Context(), map[string]any{"operation": "cancel", "job_id": asyncStart.JobID})
	if err != nil {
		t.Fatal(err)
	}
	var asyncCancel jobs.CancelResult
	if err = json.Unmarshal(raw, &asyncCancel); err != nil {
		t.Fatal(err)
	}
	if !asyncCancel.Canceled || asyncCancel.JobID != asyncStart.JobID ||
		asyncCancel.Status.Outcome != jobs.OutcomeCanceled || asyncCancel.Status.Cleanup != jobs.CleanupComplete {
		t.Fatalf("async cancel = %#v", asyncCancel)
	}
	select {
	case <-asyncCanceled:
	case <-time.After(time.Second):
		t.Fatal("async cancel did not reach launcher-owned runner")
	}

	runner.setFailure(errors.New("synthetic launcher rejection"))
	raw, err = client.Call(t.Context(), map[string]any{
		"operation": "run", "cwd": ".", "argv": []string{"/bin/false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result = nil
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result["outcome"] != string(jobs.OutcomeLaunchFailed) || result["cleanup"] != string(jobs.CleanupNotRequired) {
		t.Fatalf("launcher failure result = %#v", result)
	}
	if _, present := result["exit_code"]; present {
		t.Fatalf("launcher failure invented exit code: %#v", result)
	}

	started, canceledByLauncher := runner.setWait()
	callCtx, cancelCall := context.WithCancel(t.Context())
	callDone := make(chan error, 1)
	go func() {
		_, err := client.Call(callCtx, map[string]any{
			"operation": "run", "cwd": ".", "argv": []string{"/bin/sleep", "10"},
		})
		callDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cancel fixture did not reach sandbox runner")
	}
	cancelCall()
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("canceled executor client returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("executor client did not stop after cancellation")
	}
	select {
	case <-canceledByLauncher:
	case <-time.After(time.Second):
		t.Fatal("executor client cancellation did not cancel launcher-owned workload")
	}

	cancel()
	for role, done := range map[string]<-chan error{"executor": executorDone, "launcher": launcherDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s shutdown: %v", role, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s shutdown timeout", role)
		}
	}
	for _, socket := range []string{executorSocket, launcherSocket} {
		if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("role socket retained %q: %v", socket, err)
		}
	}
}
