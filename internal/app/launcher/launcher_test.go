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
	"loki/internal/work/jobs"
)

type fakeRunner struct {
	mu sync.Mutex

	startCalls   int
	observeCalls int
	cleanupCalls int
	inspectCalls int

	startResult      sandbox.StartResult
	startErr         error
	observeResult    sandbox.Result
	observeErr       error
	outputBytes      []byte
	outputTruncated  bool
	outputErr        error
	endpointBindings []sandbox.EndpointBinding
	endpointErr      error
	cleanupResult    sandbox.CleanupStatus
	cleanupErr       error
	inspectState     sandbox.ResourceState
	inspectErr       error

	blockObserve bool
	started      chan struct{}
	observing    chan struct{}
	canceled     chan struct{}
	startOnce    sync.Once
	observeOnce  sync.Once
	cancelOnce   sync.Once
}

func fakeInstanceRef() string {
	return "oci-instance-sha256:" + strings.Repeat("1", 64)
}

func (r *fakeRunner) StartJob(_ context.Context, plan sandbox.Plan) (sandbox.StartResult, error) {
	r.mu.Lock()
	r.startCalls++
	result, err := r.startResult, r.startErr
	endpointBindings := append([]sandbox.EndpointBinding(nil), r.endpointBindings...)
	r.mu.Unlock()
	if r.started != nil {
		r.startOnce.Do(func() { close(r.started) })
	}
	if !result.Resource.Valid() {
		result.Resource = plan.Resource()
	}
	if err == nil && !result.Created && !result.Started {
		result.Created = true
		result.Started = true
	}
	if result.Created && result.InstanceRef == "" {
		result.InstanceRef = fakeInstanceRef()
	}
	if len(result.EndpointBindings) == 0 && len(endpointBindings) != 0 {
		result.EndpointBindings = endpointBindings
	}
	return result, err
}

func (r *fakeRunner) EndpointBindings(
	_ context.Context, _ sandbox.Resource, _ string, _ []sandbox.EndpointSpec,
) ([]sandbox.EndpointBinding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sandbox.EndpointBinding(nil), r.endpointBindings...), r.endpointErr
}

func (r *fakeRunner) OutputJob(ctx context.Context, resource sandbox.Resource, instanceRef string) ([]byte, bool, error) {
	return r.OutputJobLimit(ctx, resource, instanceRef, jobs.MaxOutputBytes)
}

func (r *fakeRunner) OutputJobLimit(_ context.Context, _ sandbox.Resource, _ string, maximum int) ([]byte, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	output := append([]byte(nil), r.outputBytes...)
	truncated := r.outputTruncated
	if len(output) > maximum {
		output = output[:maximum]
		truncated = true
	}
	return output, truncated, r.outputErr
}

func (r *fakeRunner) ObserveJob(ctx context.Context, _ sandbox.Resource, _ string) (sandbox.Result, error) {
	r.mu.Lock()
	r.observeCalls++
	block := r.blockObserve
	result, err := r.observeResult, r.observeErr
	r.mu.Unlock()
	if r.observing != nil {
		r.observeOnce.Do(func() { close(r.observing) })
	}
	if block {
		<-ctx.Done()
		if r.canceled != nil {
			r.cancelOnce.Do(func() { close(r.canceled) })
		}
		outcome := sandbox.OutcomeCanceled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			outcome = sandbox.OutcomeTimedOut
		}
		return sandbox.Result{Outcome: outcome, Cleanup: sandbox.CleanupPending}, nil
	}
	if !result.Outcome.Valid() {
		result = sandbox.Result{
			ExitCode:      0,
			ExitCodeKnown: true,
			Outcome:       sandbox.OutcomeExited,
			Cleanup:       sandbox.CleanupPending,
		}
	}
	return result, err
}

func (r *fakeRunner) CleanupJob(_ context.Context, _ sandbox.Resource, _ string) (sandbox.CleanupStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanupCalls++
	result := r.cleanupResult
	if !result.Valid() || result == sandbox.CleanupPending {
		result = sandbox.CleanupComplete
	}
	return result, r.cleanupErr
}

func (r *fakeRunner) Inspect(_ context.Context, _ sandbox.Resource) (sandbox.ResourceState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inspectCalls++
	return r.inspectState, r.inspectErr
}

func (r *fakeRunner) InspectJob(_ context.Context, _ sandbox.Resource, _ string) (sandbox.ResourceState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inspectCalls++
	return r.inspectState, r.inspectErr
}

func (r *fakeRunner) counts() (start, observe, cleanup, inspect int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startCalls, r.observeCalls, r.cleanupCalls, r.inspectCalls
}

func launcherPolicy(t *testing.T) sandbox.Policy {
	t.Helper()
	inputDirectory := filepath.Join(t.TempDir(), "run-inputs")
	if err := os.Mkdir(inputDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	policy, err := sandbox.NewPolicy(sandbox.PolicyOptions{
		GenerationSHA256: strings.Repeat("a", 64),
		Image:            "registry.example/loki@sha256:" + strings.Repeat("b", 64),
		InputDirectory:   inputDirectory,
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

func launcherJournal(t *testing.T, retention time.Duration, maxJobs int) *jobs.Journal {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "jobs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := jobs.OpenJournal(dir, jobs.JournalLimits{
		MaxRecords: maxJobs, MaxRecordBytes: 128 << 10, MaxOutputBytes: 4096, Retention: retention,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close journal: %v", err)
		}
	})
	return journal
}

func lifecycleFixture(t *testing.T, runner Runner, timeout, retention time.Duration, maxJobs int) *lifecycle {
	t.Helper()
	journal := launcherJournal(t, retention, maxJobs)
	l, err := newLifecycle(t.Context(), launcherPolicy(t), runner, nil, journal, timeout)
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

func TestPrepareRunInputUsesLauncherOwnedReadOnlyFile(t *testing.T) {
	l := lifecycleFixture(t, &fakeRunner{}, time.Second, time.Second, 8)
	id := strings.Repeat("d", 32)
	path, err := l.prepareRunInput(id, []byte{'a', 0, 'b'})
	if err != nil {
		t.Fatal(err)
	}
	if path != l.runInputPath(id) {
		t.Fatalf("input path = %q, want %q", path, l.runInputPath(id))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string([]byte{'a', 0, 'b'}) {
		t.Fatalf("input = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0444 {
		t.Fatalf("input mode = %04o", info.Mode().Perm())
	}
	l.removeRunInput(id)
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed input stat = %v", err)
	}
}

func TestLifecycleOperationsExposeOnlyExecutorJobLifecycle(t *testing.T) {
	l := lifecycleFixture(t, &fakeRunner{}, time.Second, time.Second, 8)
	operations := l.operations()
	if len(operations) != 6 {
		t.Fatalf("operations = %#v", operations)
	}
	for _, name := range []string{"run", "start", "inspect", "output", "wait", "cancel"} {
		operation, ok := operations[name]
		if !ok || operation.Grant != controlpolicy.WorkloadLaunch {
			t.Fatalf("%s operation = %#v", name, operation)
		}
	}
	if operations["run"].Timeout != time.Second || operations["wait"].Timeout != time.Second {
		t.Fatalf("run/wait timeouts = %v/%v", operations["run"].Timeout, operations["wait"].Timeout)
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
	start, _, _, _ := runner.counts()
	if start != 0 {
		t.Fatalf("runner start calls = %d", start)
	}
}

func TestStartRejectsNetworkAuthorityFieldsBeforeRunner(t *testing.T) {
	runner := &fakeRunner{}
	l := lifecycleFixture(t, runner, time.Second, time.Second, 8)
	operation := l.operations()["start"]
	base := map[string]any{
		"operation": "start", "id": strings.Repeat("c", 32),
		"policy_sha256": strings.Repeat("a", 64), "cwd": ".", "argv": []string{"/bin/true"},
		"network":   "dependency-install",
		"endpoints": []map[string]any{{"name": "web", "port": 5173}},
	}
	for _, injected := range []map[string]any{
		{"host_port": 43001},
		{"proxy_token": "opaque-value"},
		{"proxy_url": "http://opaque.invalid"},
		{"network_id": strings.Repeat("f", 64)},
		{"network_name": "backend-network"},
		{"gateway_image": "registry.example/gateway@sha256:" + strings.Repeat("e", 64)},
		{"backend_ref": "private-backend"},
		{"sandbox_sha256": strings.Repeat("d", 64)},
		{"endpoints": []map[string]any{{"name": "web", "port": 5173, "host_port": 43001}}},
	} {
		request := map[string]any{}
		for key, value := range base {
			request[key] = value
		}
		for key, value := range injected {
			request[key] = value
		}
		if _, err := operation.Handle(t.Context(), encodedRequest(t, request)); err == nil {
			t.Fatalf("authority field was accepted: %#v", injected)
		}
	}
	start, _, _, _ := runner.counts()
	if start != 0 {
		t.Fatalf("runner start calls = %d", start)
	}
}

func TestAsyncStartRejectsForgedReplayIdentityBeforeRunner(t *testing.T) {
	requestID := "123e4567-e89b-12d3-a456-426614174019"
	id, err := jobs.JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	normalized, fingerprint, err := jobs.NormalizeStartRequest(jobs.StartRequest{
		RequestID: requestID, CWD: ".", Argv: []string{"/bin/true"}, TimeoutSeconds: 3,
		Network:   jobs.NetworkDependencyInstall,
		Endpoints: []jobs.EndpointRequest{{Name: "web", Port: 5173}},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := jobs.Workload{
		ID: id, RequestID: normalized.RequestID, RequestSHA256: fingerprint,
		CWD: normalized.CWD, Argv: normalized.Argv, TimeoutSeconds: normalized.TimeoutSeconds,
		Network: normalized.Network, Endpoints: normalized.Endpoints,
	}
	tests := []struct {
		name   string
		mutate func(*jobs.Workload)
	}{
		{"job-id", func(workload *jobs.Workload) { workload.ID = strings.Repeat("e", 32) }},
		{"fingerprint", func(workload *jobs.Workload) { workload.RequestSHA256 = strings.Repeat("f", 64) }},
		{"endpoint-with-stale-fingerprint", func(workload *jobs.Workload) {
			workload.Endpoints = []jobs.EndpointRequest{{Name: "web", Port: 5174}}
		}},
		{"network-with-stale-fingerprint", func(workload *jobs.Workload) {
			workload.Network = jobs.NetworkNone
		}},
		{"missing-request-id", func(workload *jobs.Workload) { workload.RequestID = "" }},
		{"missing-fingerprint", func(workload *jobs.Workload) { workload.RequestSHA256 = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{}
			l := lifecycleFixture(t, runner, 5*time.Second, time.Second, 8)
			workload := base
			workload.Argv = append([]string(nil), base.Argv...)
			workload.Endpoints = append([]jobs.EndpointRequest(nil), base.Endpoints...)
			test.mutate(&workload)
			if _, err := l.startWorkload(t.Context(), workload, strings.Repeat("a", 64)); err == nil {
				t.Fatal("forged replay identity was accepted")
			}
			start, _, _, _ := runner.counts()
			if start != 0 {
				t.Fatalf("backend starts = %d", start)
			}
		})
	}
}

func TestAsyncStartReplaysSameRequestAndConflictsChangedInput(t *testing.T) {
	requestID := "123e4567-e89b-12d3-a456-426614174000"
	id, err := jobs.JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := jobs.StartFingerprint(".", []string{"/bin/true"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		blockObserve: true, observing: make(chan struct{}), canceled: make(chan struct{}),
	}
	l := lifecycleFixture(t, runner, 5*time.Second, time.Second, 8)
	workload := jobs.Workload{
		ID: id, RequestID: requestID, RequestSHA256: fingerprint,
		CWD: ".", Argv: []string{"/bin/true"}, TimeoutSeconds: 3,
	}
	first, err := l.startWorkload(t.Context(), workload, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || !first.Detached || first.RequestID != requestID || first.JobID != id {
		t.Fatalf("first start = %#v", first)
	}
	select {
	case <-runner.observing:
	case <-time.After(time.Second):
		t.Fatal("asynchronous workload did not start")
	}

	second, err := l.startWorkload(t.Context(), workload, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.JobID != id || second.DeadlineAt != first.DeadlineAt {
		t.Fatalf("replayed start = %#v", second)
	}
	starts, _, _, _ := runner.counts()
	if starts != 1 {
		t.Fatalf("backend starts = %d", starts)
	}

	changed := workload
	changed.TimeoutSeconds = 2
	changed.RequestSHA256, err = jobs.StartFingerprint(changed.CWD, changed.Argv, changed.TimeoutSeconds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.startWorkload(t.Context(), changed, strings.Repeat("a", 64)); err == nil {
		t.Fatal("changed-input request replay was accepted")
	}
	starts, _, _, _ = runner.counts()
	if starts != 1 {
		t.Fatalf("conflicting replay started backend: %d", starts)
	}

	cancelCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err = l.cancelStatus(cancelCtx, id); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncInspectOutputAndCancelAreNonAuthorityBearing(t *testing.T) {
	requestID := "123e4567-e89b-12d3-a456-426614174001"
	id, err := jobs.JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := jobs.StartFingerprint(".", []string{"/bin/sleep", "10"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		blockObserve: true, observing: make(chan struct{}), canceled: make(chan struct{}),
		outputBytes: []byte("live-output"), outputTruncated: true,
	}
	l := lifecycleFixture(t, runner, 5*time.Second, time.Second, 8)
	if _, err = l.startWorkload(t.Context(), jobs.Workload{
		ID: id, RequestID: requestID, RequestSHA256: fingerprint,
		CWD: ".", Argv: []string{"/bin/sleep", "10"}, TimeoutSeconds: 5,
	}, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.observing:
	case <-time.After(time.Second):
		t.Fatal("asynchronous workload did not enter observation")
	}

	status, err := l.inspect(id)
	if err != nil {
		t.Fatal(err)
	}
	if status.JobID != id || status.State != jobs.StateRunning || status.Outcome != "" {
		t.Fatalf("status = %#v", status)
	}
	output, err := l.output(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if output.JobID != id || output.State != jobs.StateRunning ||
		output.Output != "live-output" || !output.Truncated || output.Complete {
		t.Fatalf("output = %#v", output)
	}

	cancelCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	firstCancel, err := l.cancelStatus(cancelCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !firstCancel.Canceled || firstCancel.Status.Outcome != jobs.OutcomeCanceled ||
		firstCancel.Status.Cleanup != jobs.CleanupComplete {
		t.Fatalf("first cancel = %#v", firstCancel)
	}
	secondCancel, err := l.cancelStatus(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if secondCancel.Canceled || secondCancel.Status.State != jobs.StateTerminal {
		t.Fatalf("second cancel = %#v", secondCancel)
	}
}

func TestAsyncStartRejectsRequestedLifetimeAboveTrustedLimit(t *testing.T) {
	requestID := "123e4567-e89b-12d3-a456-426614174002"
	id, err := jobs.JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := jobs.StartFingerprint(".", []string{"/bin/true"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	l := lifecycleFixture(t, runner, time.Second, time.Second, 8)
	if _, err = l.startWorkload(t.Context(), jobs.Workload{
		ID: id, RequestID: requestID, RequestSHA256: fingerprint,
		CWD: ".", Argv: []string{"/bin/true"}, TimeoutSeconds: 2,
	}, strings.Repeat("a", 64)); err == nil {
		t.Fatal("requested lifetime above trusted launcher timeout was accepted")
	}
	starts, _, _, _ := runner.counts()
	if starts != 0 {
		t.Fatalf("backend starts = %d", starts)
	}
}

func TestAsyncEndpointLeasesBindBeforeRunningAndReleaseOnCancel(t *testing.T) {
	requestID := "123e4567-e89b-12d3-a456-426614174020"
	id, err := jobs.JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	normalized, fingerprint, err := jobs.NormalizeStartRequest(jobs.StartRequest{
		RequestID: requestID, CWD: ".", Argv: []string{"/bin/sleep", "10"},
		Network:   jobs.NetworkDependencyInstall,
		Endpoints: []jobs.EndpointRequest{{Name: "web", Port: 5173}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		blockObserve: true, observing: make(chan struct{}), canceled: make(chan struct{}),
		endpointBindings: []sandbox.EndpointBinding{{Name: "web", Port: 5173, HostPort: 43001}},
	}
	l := lifecycleFixture(t, runner, 5*time.Second, time.Second, 8)
	if _, err = l.startWorkload(t.Context(), jobs.Workload{
		ID: id, RequestID: requestID, RequestSHA256: fingerprint,
		CWD: normalized.CWD, Argv: normalized.Argv, TimeoutSeconds: normalized.TimeoutSeconds,
		Network: normalized.Network, Endpoints: normalized.Endpoints,
	}, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.observing:
	case <-time.After(time.Second):
		t.Fatal("endpoint workload did not start")
	}
	status, err := l.inspect(id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != jobs.StateRunning || status.Network != jobs.NetworkDependencyInstall ||
		len(status.Endpoints) != 1 || status.Endpoints[0].State != jobs.EndpointLeaseActive ||
		status.Endpoints[0].HostPort != 43001 {
		t.Fatalf("running endpoint status = %#v", status)
	}
	cancelCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := l.cancelStatus(cancelCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Canceled || len(result.Status.Endpoints) != 1 ||
		result.Status.Endpoints[0].State != jobs.EndpointLeaseReleased {
		t.Fatalf("cancel endpoint status = %#v", result)
	}
}

func TestReconcileRejectsChangedEndpointHostPortAndRevokesLease(t *testing.T) {
	requestID := "123e4567-e89b-12d3-a456-426614174021"
	id, err := jobs.JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	policy := launcherPolicy(t)
	normalized, fingerprint, err := jobs.NormalizeStartRequest(jobs.StartRequest{
		RequestID: requestID, CWD: ".", Argv: []string{"/bin/sleep", "10"},
		Endpoints: []jobs.EndpointRequest{{Name: "web", Port: 5173}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := policy.Plan(sandbox.WorkloadSpec{
		ID: id, PolicySHA256: strings.Repeat("a", 64), CWD: normalized.CWD,
		Argv: normalized.Argv, Network: sandbox.NetworkNone,
		Endpoints: []sandbox.EndpointSpec{{Name: "web", Port: 5173}},
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := launcherJournal(t, time.Second, 8)
	now := time.Now().UTC()
	if _, _, err = journal.AdmitRequestWithIntent(
		id, backendReference(plan), requestID, fingerprint,
		normalized.Network, normalized.Endpoints, now.Add(time.Minute), now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(id, fakeInstanceRef(), now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindEndpointLeases(id, []jobs.EndpointLease{
		{Name: "web", Port: 5173, HostPort: 43001},
	}, now.Add(2*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkRunning(id, now.Add(3*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{
		inspectState:     sandbox.ResourceState{Exists: true, Running: true},
		endpointBindings: []sandbox.EndpointBinding{{Name: "web", Port: 5173, HostPort: 43002}},
		cleanupResult:    sandbox.CleanupComplete,
	}
	l, err := newLifecycle(t.Context(), policy, runner, nil, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	if err = l.reconcile(); err != nil {
		t.Fatal(err)
	}
	status, err := l.inspect(id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != jobs.StateTerminal || status.Outcome != jobs.OutcomeUnknown ||
		status.Cleanup != jobs.CleanupComplete || len(status.Endpoints) != 1 ||
		status.Endpoints[0].State != jobs.EndpointLeaseReleased {
		t.Fatalf("reconciled endpoint status = %#v", status)
	}
	start, observe, cleanup, inspect := runner.counts()
	if start != 0 || observe != 0 || cleanup != 1 || inspect != 1 {
		t.Fatalf("reconcile calls start=%d observe=%d cleanup=%d inspect=%d", start, observe, cleanup, inspect)
	}
}

func TestStartWaitRetainsTerminalResultAndOutput(t *testing.T) {
	id := strings.Repeat("c", 32)
	runner := &fakeRunner{
		observeResult: sandbox.Result{
			ExitCode: 7, ExitCodeKnown: true, Outcome: sandbox.OutcomeExited,
			Output: []byte("hello"), Cleanup: sandbox.CleanupPending,
		},
	}
	l := lifecycleFixture(t, runner, time.Second, time.Second, 8)
	if err := l.start(launcherSpec(id)); err != nil {
		t.Fatal(err)
	}
	first, err := l.wait(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.wait(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for index, result := range []jobs.Result{first, second} {
		if result.ExitCode == nil || *result.ExitCode != 7 || result.Outcome != jobs.OutcomeExited ||
			result.Output.Text != "hello" || result.Cleanup != jobs.CleanupComplete {
			t.Fatalf("result[%d] = %#v", index, result)
		}
	}
	start, observe, cleanup, _ := runner.counts()
	if start != 1 || observe != 1 || cleanup != 1 {
		t.Fatalf("calls start=%d observe=%d cleanup=%d", start, observe, cleanup)
	}
}

func TestWaitOperationReturnsDurableOutcomeMetadata(t *testing.T) {
	id := strings.Repeat("c", 32)
	runner := &fakeRunner{
		observeResult: sandbox.Result{
			ExitCode: 137, ExitCodeKnown: true, Outcome: sandbox.OutcomeOOMKilled,
			Output: []byte("oom"), OutputTruncated: true, Cleanup: sandbox.CleanupPending,
		},
	}
	l := lifecycleFixture(t, runner, time.Second, time.Second, 8)
	if err := l.start(launcherSpec(id)); err != nil {
		t.Fatal(err)
	}
	value, err := l.operations()["wait"].Handle(t.Context(), encodedRequest(t, map[string]any{
		"operation": "wait",
		"id":        id,
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, ok := value.(waitResult)
	if !ok || result.ExitCode == nil || *result.ExitCode != 137 || result.Outcome != jobs.OutcomeOOMKilled ||
		result.Output != "oom" || !result.Truncated || result.Cleanup != jobs.CleanupComplete {
		t.Fatalf("wire result = %#v", value)
	}
}

func TestWaitTimeoutDoesNotCancelJobButExplicitCancelDoes(t *testing.T) {
	id := strings.Repeat("c", 32)
	runner := &fakeRunner{
		blockObserve: true, observing: make(chan struct{}), canceled: make(chan struct{}),
	}
	l := lifecycleFixture(t, runner, 5*time.Second, time.Second, 8)
	if err := l.start(launcherSpec(id)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.observing:
	case <-time.After(time.Second):
		t.Fatal("runner did not enter observation")
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
	result, err := l.wait(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != jobs.OutcomeCanceled || result.Cleanup != jobs.CleanupComplete {
		t.Fatalf("cancel result = %#v", result)
	}
}

func TestDuplicateCapacityAndRetentionAreBounded(t *testing.T) {
	first := strings.Repeat("c", 32)
	second := strings.Repeat("d", 32)
	runner := &fakeRunner{blockObserve: true, observing: make(chan struct{})}
	l := lifecycleFixture(t, runner, 5*time.Second, 20*time.Millisecond, 1)
	if err := l.start(launcherSpec(first)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.observing:
	case <-time.After(time.Second):
		t.Fatal("first job did not start")
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
	time.Sleep(40 * time.Millisecond)

	runner.mu.Lock()
	runner.blockObserve = false
	runner.observing = nil
	runner.canceled = nil
	runner.mu.Unlock()
	if err := l.start(launcherSpec(second)); err != nil {
		t.Fatalf("expired result did not release capacity: %v", err)
	}
	if _, err := l.wait(t.Context(), second); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileResumesWithoutDuplicateStartAndKeepsOriginalDeadline(t *testing.T) {
	id := strings.Repeat("c", 32)
	policy := launcherPolicy(t)
	journal := launcherJournal(t, time.Second, 8)
	now := time.Now().UTC()
	deadline := now.Add(80 * time.Millisecond)
	plan, err := policy.Plan(launcherSpec(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Admit(id, backendReference(plan), deadline, now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(id, fakeInstanceRef(), now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkRunning(id, now.Add(2*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{
		inspectState: sandbox.ResourceState{Exists: true, Running: true},
		blockObserve: true,
		canceled:     make(chan struct{}),
	}
	l, err := newLifecycle(t.Context(), policy, runner, nil, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	if err = l.reconcile(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.canceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("recovered job did not honor original deadline")
	}
	result, err := l.wait(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != jobs.OutcomeTimedOut || result.Cleanup != jobs.CleanupComplete {
		t.Fatalf("recovered result = %#v", result)
	}
	start, _, _, inspect := runner.counts()
	if start != 0 || inspect != 1 {
		t.Fatalf("reconcile calls start=%d inspect=%d", start, inspect)
	}
}

func TestReconcileMarksMissingUncertainStartOutcomeUnknown(t *testing.T) {
	id := strings.Repeat("c", 32)
	policy := launcherPolicy(t)
	journal := launcherJournal(t, time.Second, 8)
	now := time.Now().UTC()
	plan, err := policy.Plan(launcherSpec(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Admit(id, backendReference(plan), now.Add(time.Second), now); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{inspectState: sandbox.ResourceState{}}
	l, err := newLifecycle(t.Context(), policy, runner, nil, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	if err = l.reconcile(); err != nil {
		t.Fatal(err)
	}
	result, err := l.wait(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != jobs.OutcomeUnknown || result.Cleanup != jobs.CleanupComplete || result.ExitCode != nil {
		t.Fatalf("missing recovered result = %#v", result)
	}
	start, observe, cleanup, inspect := runner.counts()
	if start != 0 || observe != 0 || cleanup != 0 || inspect != 1 {
		t.Fatalf("calls start=%d observe=%d cleanup=%d inspect=%d", start, observe, cleanup, inspect)
	}
}

func TestReconcileDoesNotAdoptUnboundExistingResource(t *testing.T) {
	id := strings.Repeat("c", 32)
	policy := launcherPolicy(t)
	journal := launcherJournal(t, time.Second, 8)
	now := time.Now().UTC()
	plan, err := policy.Plan(launcherSpec(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Admit(id, backendReference(plan), now.Add(time.Second), now); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{inspectState: sandbox.ResourceState{Exists: true, Running: true}}
	l, err := newLifecycle(t.Context(), policy, runner, nil, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	if err = l.reconcile(); err != nil {
		t.Fatal(err)
	}
	result, err := l.wait(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != jobs.OutcomeUnknown || result.Cleanup != jobs.CleanupFailed || result.ExitCode != nil {
		t.Fatalf("unbound recovery result = %#v", result)
	}
	start, observe, cleanup, inspect := runner.counts()
	if start != 0 || observe != 0 || cleanup != 0 || inspect != 1 {
		t.Fatalf("unbound recovery calls start=%d observe=%d cleanup=%d inspect=%d", start, observe, cleanup, inspect)
	}
}

func TestReconcileRejectsExactInstanceReplacement(t *testing.T) {
	id := strings.Repeat("c", 32)
	policy := launcherPolicy(t)
	journal := launcherJournal(t, time.Second, 8)
	now := time.Now().UTC()
	plan, err := policy.Plan(launcherSpec(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Admit(id, backendReference(plan), now.Add(time.Second), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(id, fakeInstanceRef(), now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkRunning(id, now.Add(2*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{inspectErr: sandbox.ErrInstanceMismatch}
	l, err := newLifecycle(t.Context(), policy, runner, nil, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	if err = l.reconcile(); err != nil {
		t.Fatal(err)
	}
	result, err := l.wait(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != jobs.OutcomeUnknown || result.Cleanup != jobs.CleanupFailed || result.ExitCode != nil {
		t.Fatalf("replacement result = %#v", result)
	}
	start, observe, cleanup, inspect := runner.counts()
	if start != 0 || observe != 0 || cleanup != 0 || inspect != 1 {
		t.Fatalf("replacement calls start=%d observe=%d cleanup=%d inspect=%d", start, observe, cleanup, inspect)
	}
}

func TestReconcileRetainsAndRetriesCleanupDebt(t *testing.T) {
	id := strings.Repeat("c", 32)
	policy := launcherPolicy(t)
	journal := launcherJournal(t, time.Second, 8)
	now := time.Now().UTC()
	plan, err := policy.Plan(launcherSpec(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Admit(id, backendReference(plan), now.Add(time.Second), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(id, fakeInstanceRef(), now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	exitCode := int64(0)
	if _, err = journal.MarkTerminal(id, jobs.Result{
		ExitCode: &exitCode, Outcome: jobs.OutcomeExited, Cleanup: jobs.CleanupPending,
	}, now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkCleanup(id, jobs.CleanupFailed, now.Add(2*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{cleanupResult: sandbox.CleanupComplete}
	l, err := newLifecycle(t.Context(), policy, runner, nil, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	if err = l.reconcile(); err != nil {
		t.Fatal(err)
	}
	result, err := l.wait(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cleanup != jobs.CleanupComplete {
		t.Fatalf("cleanup debt result = %#v", result)
	}
	_, _, cleanup, _ := runner.counts()
	if cleanup != 1 {
		t.Fatalf("cleanup retries = %d", cleanup)
	}
}

func TestCloseCancelsOwnedJobs(t *testing.T) {
	id := strings.Repeat("c", 32)
	runner := &fakeRunner{
		blockObserve: true, observing: make(chan struct{}), canceled: make(chan struct{}),
	}
	journal := launcherJournal(t, time.Second, 8)
	l, err := newLifecycle(t.Context(), launcherPolicy(t), runner, nil, journal, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.start(launcherSpec(id)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.observing:
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

func TestLifecycleRequiresValidatedInputs(t *testing.T) {
	runner := &fakeRunner{}
	journal := launcherJournal(t, time.Second, 8)
	if _, err := newLifecycle(t.Context(), sandbox.Policy{}, runner, nil, journal, time.Second); err == nil {
		t.Fatal("zero sandbox policy was accepted")
	}
	if _, err := newLifecycle(t.Context(), launcherPolicy(t), nil, nil, journal, time.Second); err == nil {
		t.Fatal("nil runner was accepted")
	}
	if _, err := newLifecycle(t.Context(), launcherPolicy(t), runner, nil, nil, time.Second); err == nil {
		t.Fatal("nil journal was accepted")
	}
	if _, err := newLifecycle(t.Context(), launcherPolicy(t), runner, nil, journal, 0); err == nil {
		t.Fatal("zero timeout was accepted")
	}
	if err := Run(t.Context(), Options{
		Socket:      filepath.Join(t.TempDir(), "launcher.sock"),
		SocketGID:   os.Getgid(),
		ExecutorUID: 0,
		Policy:      launcherPolicy(t),
		Runner:      runner,
		Journal:     journal,
		RunTimeout:  time.Second,
	}); err == nil {
		t.Fatal("root executor UID was accepted")
	}
}
