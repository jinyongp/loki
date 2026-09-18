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
	mu    sync.Mutex
	plans []sandbox.Plan
	err   error
	wait  bool
}

func (r *recordingSandboxRunner) Run(ctx context.Context, plan sandbox.Plan) (sandbox.Result, error) {
	r.mu.Lock()
	r.plans = append(r.plans, plan)
	err, wait := r.err, r.wait
	r.mu.Unlock()
	if wait {
		<-ctx.Done()
		return sandbox.Result{}, ctx.Err()
	}
	if err != nil {
		return sandbox.Result{}, err
	}
	return sandbox.Result{ExitCode: 17}, nil
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
	r.err, r.wait = err, false
}

func (r *recordingSandboxRunner) setWait() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err, r.wait = nil, true
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

	root := t.TempDir()
	launcherSocket := filepath.Join(root, "launcher", "control.sock")
	executorSocket := filepath.Join(root, "executor", "control.sock")
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
			RunTimeout:  time.Second,
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
		Timeout:      3 * time.Second,
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
			RunTimeout: 4 * time.Second,
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
	if len(result) != 2 || !regexp.MustCompile("^[0-9a-f]{32}$").MatchString(jobID) || exitCode != 17 {
		t.Fatalf("executor result = %#v", result)
	}
	if runner.calls() != 1 {
		t.Fatalf("runner calls = %d", runner.calls())
	}
	plan := runner.plan(0)
	if plan.Name() != "loki-job-"+jobID || plan.PolicySHA256() != policyDigest {
		t.Fatalf("launcher plan name=%q policy=%q", plan.Name(), plan.PolicySHA256())
	}

	runner.setFailure(errors.New("synthetic launcher rejection"))
	if _, err := client.Call(t.Context(), map[string]any{
		"operation": "run", "cwd": ".", "argv": []string{"/bin/false"},
	}); err == nil {
		t.Fatal("launcher rejection became executor success")
	}

	runner.setWait()
	if _, err := client.Call(t.Context(), map[string]any{
		"operation": "run", "cwd": ".", "argv": []string{"/bin/sleep", "10"},
	}); err == nil {
		t.Fatal("launcher timeout became executor success")
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
