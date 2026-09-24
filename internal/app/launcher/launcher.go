// Package launcher owns the narrow privileged workload-launch role.
package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/fault"
	"loki/internal/platform/sandbox"
	"loki/internal/rpc"
	"loki/internal/work/jobs"
)

const (
	maxRunTimeout              = 24 * time.Hour
	launcherStopTimeout        = 30 * time.Second
	reconcileRetryDelay        = 100 * time.Millisecond
	defaultMaxConcurrentJobs   = 16
	maxSupportedConcurrentJobs = 256
)

var launcherJobIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Runner interface {
	StartJob(context.Context, sandbox.Plan) (sandbox.StartResult, error)
	EndpointBindings(context.Context, sandbox.Resource, string, []sandbox.EndpointSpec) ([]sandbox.EndpointBinding, error)
	OutputJob(context.Context, sandbox.Resource, string) ([]byte, bool, error)
	OutputJobLimit(context.Context, sandbox.Resource, string, int) ([]byte, bool, error)
	ObserveJob(context.Context, sandbox.Resource, string) (sandbox.Result, error)
	CleanupJob(context.Context, sandbox.Resource, string) (sandbox.CleanupStatus, error)
	Inspect(context.Context, sandbox.Resource) (sandbox.ResourceState, error)
	InspectJob(context.Context, sandbox.Resource, string) (sandbox.ResourceState, error)
}

type ResolvedToolchains struct {
	Mounts  []sandbox.ToolchainMount
	Close   func() error
	Discard func() error
}

type ToolchainResolver interface {
	Resolve(context.Context, string, []jobs.ToolchainRef) (ResolvedToolchains, error)
	Cleanup(string, []jobs.ToolchainRef) error
}

type Options struct {
	Socket            string
	SocketGID         int
	ExecutorUID       uint32
	Policy            sandbox.Policy
	Runner            Runner
	Toolchains        ToolchainResolver
	Journal           *jobs.Journal
	RunTimeout        time.Duration
	MaxConcurrentJobs int
	Ready             func() error
}

type lifecycle struct {
	ctx               context.Context
	cancel            context.CancelFunc
	policy            sandbox.Policy
	runner            Runner
	toolchains        ToolchainResolver
	journal           *jobs.Journal
	runInputDirectory string
	timeout           time.Duration
	maxConcurrentJobs int
	now               func() time.Time

	mu     sync.Mutex
	active map[string]*ownedJob
	runs   map[string]*ownedJob
	closed bool
	wg     sync.WaitGroup
}

type ownedJob struct {
	cancel          context.CancelFunc
	done            chan struct{}
	err             error
	rawOutputLimit  int
	rawResult       *jobs.RunExecutionResult
	closeToolchains func() error
	toolchains      []jobs.ToolchainRef
}

type runRequest struct {
	ID             string   `json:"id"`
	PolicySHA256   string   `json:"policy_sha256"`
	CWD            string   `json:"cwd"`
	Argv           []string `json:"argv"`
	Input          []byte   `json:"input,omitempty"`
	MaxOutputBytes int      `json:"max_output_bytes"`
}

type startRequest struct {
	ID             string                 `json:"id"`
	RequestID      string                 `json:"request_id,omitempty"`
	RequestSHA256  string                 `json:"request_sha256,omitempty"`
	PolicySHA256   string                 `json:"policy_sha256"`
	CWD            string                 `json:"cwd"`
	Argv           []string               `json:"argv"`
	TimeoutSeconds int                    `json:"timeout_seconds,omitempty"`
	Network        jobs.NetworkProfile    `json:"network,omitempty"`
	Endpoints      []jobs.EndpointRequest `json:"endpoints,omitempty"`
	Toolchains     []jobs.ToolchainRef    `json:"toolchains,omitempty"`
}

type waitRequest struct {
	ID string `json:"id"`
}

type waitResult struct {
	ExitCode  *int64             `json:"exit_code,omitempty"`
	Outcome   jobs.Outcome       `json:"outcome"`
	Output    string             `json:"output"`
	Truncated bool               `json:"truncated"`
	Cleanup   jobs.CleanupStatus `json:"cleanup"`
}

func newLifecycle(parent context.Context, policy sandbox.Policy, runner Runner, toolchains ToolchainResolver, journal *jobs.Journal, timeout time.Duration) (*lifecycle, error) {
	if !policy.Valid() {
		return nil, errors.New("launcher requires a valid sandbox policy")
	}
	if runner == nil {
		return nil, errors.New("launcher requires a sandbox runner")
	}
	if journal == nil || journal.MaxOutputBytes() <= 0 {
		return nil, errors.New("launcher requires a job journal")
	}
	if timeout < time.Second || timeout > maxRunTimeout {
		return nil, errors.New("launcher run timeout is outside the supported range")
	}
	ctx, cancel := context.WithCancel(parent)
	return &lifecycle{
		ctx: ctx, cancel: cancel, policy: policy, runner: runner, toolchains: toolchains, journal: journal,
		runInputDirectory: policy.InputDirectory(), timeout: timeout, maxConcurrentJobs: defaultMaxConcurrentJobs,
		now: func() time.Time { return time.Now().UTC() }, active: map[string]*ownedJob{}, runs: map[string]*ownedJob{},
	}, nil
}

func normalizeMaxConcurrentJobs(value int) (int, error) {
	if value == 0 {
		return defaultMaxConcurrentJobs, nil
	}
	if value < 1 || value > maxSupportedConcurrentJobs {
		return 0, errors.New("launcher concurrent job limit is outside the supported range")
	}
	return value, nil
}

func (l *lifecycle) operations() map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"run": {
			Grant:   controlpolicy.WorkloadLaunch,
			Timeout: l.timeout,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[runRequest](raw)
				if err != nil {
					return nil, err
				}
				return l.runWorkload(ctx, request)
			},
		},
		"start": {
			Grant: controlpolicy.WorkloadLaunch,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[startRequest](raw)
				if err != nil {
					return nil, err
				}
				return l.startWorkload(ctx, jobs.Workload{
					ID: request.ID, RequestID: request.RequestID, RequestSHA256: request.RequestSHA256,
					CWD: request.CWD, Argv: request.Argv, TimeoutSeconds: request.TimeoutSeconds,
					Network: request.Network, Endpoints: append([]jobs.EndpointRequest(nil), request.Endpoints...),
					Toolchains: append([]jobs.ToolchainRef(nil), request.Toolchains...),
				}, request.PolicySHA256)
			},
		},
		"inspect": {
			Grant: controlpolicy.WorkloadLaunch,
			Handle: func(_ context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[waitRequest](raw)
				if err != nil {
					return nil, err
				}
				return l.inspect(request.ID)
			},
		},
		"output": {
			Grant: controlpolicy.WorkloadLaunch,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[waitRequest](raw)
				if err != nil {
					return nil, err
				}
				return l.output(ctx, request.ID)
			},
		},
		"wait": {
			Grant:   controlpolicy.WorkloadLaunch,
			Timeout: l.timeout,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[waitRequest](raw)
				if err != nil {
					return nil, err
				}
				result, err := l.wait(ctx, request.ID)
				if err != nil {
					return nil, err
				}
				return toWaitResult(result), nil
			},
		},
		"cancel": {
			Grant: controlpolicy.WorkloadLaunch,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[waitRequest](raw)
				if err != nil {
					return nil, err
				}
				return l.cancelStatus(ctx, request.ID)
			},
		},
	}
}

func (l *lifecycle) runInputPath(id string) string {
	if l.runInputDirectory == "" || !launcherJobIDPattern.MatchString(id) {
		return ""
	}
	return filepath.Join(l.runInputDirectory, id+".stdin")
}

func (l *lifecycle) removeRunInput(id string) {
	if path := l.runInputPath(id); path != "" {
		_ = os.Remove(path)
	}
}

func (l *lifecycle) prepareRunInput(id string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if len(data) > jobs.MaxRunInputBytes {
		return "", fault.Error("workload request is not allowed")
	}
	path := l.runInputPath(id)
	if path == "" {
		return "", errors.New("launcher synchronous input is not configured")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(path)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Chmod(0444); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	cleanup = false
	return path, nil
}

func (l *lifecycle) lookupRun(id string) (*ownedJob, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	job, ok := l.runs[id]
	return job, ok
}

func (l *lifecycle) deleteRun(id string, job *ownedJob) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.runs[id] == job {
		delete(l.runs, id)
	}
}

func (l *lifecycle) runWorkload(ctx context.Context, request runRequest) (jobs.RunExecutionResult, error) {
	if !launcherJobIDPattern.MatchString(request.ID) ||
		len(request.Input) > jobs.MaxRunInputBytes ||
		request.MaxOutputBytes < 1 || request.MaxOutputBytes > jobs.MaxRunOutputBytes {
		return jobs.RunExecutionResult{}, fault.Error("workload request is not allowed")
	}
	inputPath, err := l.prepareRunInput(request.ID, request.Input)
	if err != nil {
		return jobs.RunExecutionResult{}, err
	}
	if inputPath != "" {
		defer os.Remove(inputPath)
	}

	if _, err = l.startWorkload(ctx, jobs.Workload{
		ID: request.ID, CWD: request.CWD, Argv: append([]string(nil), request.Argv...),
		InputPath: inputPath, MaxOutputBytes: request.MaxOutputBytes,
	}, request.PolicySHA256); err != nil {
		return jobs.RunExecutionResult{}, err
	}
	job, ok := l.lookupRun(request.ID)
	if !ok {
		return jobs.RunExecutionResult{}, errors.New("launcher synchronous job state is unavailable")
	}
	defer l.deleteRun(request.ID, job)

	select {
	case <-job.done:
		if job.rawResult != nil {
			return *job.rawResult, job.err
		}
		if job.err != nil {
			return jobs.RunExecutionResult{}, job.err
		}
		return jobs.RunExecutionResult{}, errors.New("launcher synchronous result is unavailable")
	case <-ctx.Done():
		job.cancel()
		timer := time.NewTimer(launcherStopTimeout)
		defer timer.Stop()
		select {
		case <-job.done:
		case <-timer.C:
		}
		return jobs.RunExecutionResult{}, ctx.Err()
	}
}

func (l *lifecycle) start(spec sandbox.WorkloadSpec) error {
	_, err := l.startWorkload(l.ctx, jobs.Workload{
		ID: spec.ID, CWD: spec.CWD, Argv: append([]string(nil), spec.Argv...),
	}, spec.PolicySHA256)
	return err
}

func sandboxNetworkProfile(value jobs.NetworkProfile) (sandbox.NetworkProfile, error) {
	switch value {
	case "", jobs.NetworkNone:
		return sandbox.NetworkNone, nil
	case jobs.NetworkDependencyInstall:
		return sandbox.NetworkDependencyInstall, nil
	default:
		return "", errors.New("workload network profile is invalid")
	}
}

func sandboxEndpointSpecs(values []jobs.EndpointRequest) []sandbox.EndpointSpec {
	result := make([]sandbox.EndpointSpec, len(values))
	for index, value := range values {
		result[index] = sandbox.EndpointSpec{Name: value.Name, Port: value.Port}
	}
	return result
}

func (l *lifecycle) resolveToolchains(ctx context.Context, owner string, refs []jobs.ToolchainRef) (ResolvedToolchains, error) {
	if len(refs) == 0 {
		return ResolvedToolchains{}, nil
	}
	if l.toolchains == nil {
		return ResolvedToolchains{}, errors.New("launcher toolchain resolution is not configured")
	}
	resolved, err := l.toolchains.Resolve(ctx, owner, refs)
	if err != nil {
		return ResolvedToolchains{}, err
	}
	if len(resolved.Mounts) != len(refs) || resolved.Close == nil || resolved.Discard == nil {
		if resolved.Discard != nil {
			_ = resolved.Discard()
		}
		return ResolvedToolchains{}, errors.New("launcher toolchain resolver returned an incomplete result")
	}
	families := make(map[string]bool, len(refs))
	for _, ref := range refs {
		families[ref.Family] = true
	}
	for _, mount := range resolved.Mounts {
		if !families[mount.Family] {
			_ = resolved.Discard()
			return ResolvedToolchains{}, errors.New("launcher toolchain resolver changed the requested family set")
		}
		delete(families, mount.Family)
	}
	if len(families) != 0 {
		_ = resolved.Discard()
		return ResolvedToolchains{}, errors.New("launcher toolchain resolver omitted a requested family")
	}
	return resolved, nil
}

func jobEndpointLeases(values []sandbox.EndpointBinding) []jobs.EndpointLease {
	result := make([]jobs.EndpointLease, len(values))
	for index, value := range values {
		result[index] = jobs.EndpointLease{Name: value.Name, Port: value.Port, HostPort: value.HostPort}
	}
	return result
}

func (l *lifecycle) startWorkload(ctx context.Context, workload jobs.Workload, policySHA256 string) (jobs.StartResult, error) {
	var err error
	if workload.RequestID != "" || workload.RequestSHA256 != "" {
		if workload.InputPath != "" || workload.MaxOutputBytes != 0 {
			return jobs.StartResult{}, fault.Error("replayable workload requests cannot carry synchronous-run state")
		}
		if workload.RequestID == "" || workload.RequestSHA256 == "" {
			return jobs.StartResult{}, fault.Error("workload request replay identity is invalid")
		}
		normalized, fingerprint, normalizeErr := jobs.NormalizeStartRequest(jobs.StartRequest{
			RequestID: workload.RequestID, CWD: workload.CWD,
			Argv: append([]string(nil), workload.Argv...), TimeoutSeconds: workload.TimeoutSeconds,
			Network: workload.Network, Endpoints: append([]jobs.EndpointRequest(nil), workload.Endpoints...),
			Toolchains: append([]jobs.ToolchainRef(nil), workload.Toolchains...),
		})
		if normalizeErr != nil {
			return jobs.StartResult{}, fault.Error("workload request replay identity is invalid")
		}
		expectedID, idErr := jobs.JobIDForRequestID(normalized.RequestID)
		if idErr != nil || expectedID != workload.ID || fingerprint != workload.RequestSHA256 {
			return jobs.StartResult{}, fault.Error("workload request replay identity is invalid")
		}
		workload.RequestID = normalized.RequestID
		workload.RequestSHA256 = fingerprint
		workload.CWD = normalized.CWD
		workload.Argv = normalized.Argv
		workload.TimeoutSeconds = normalized.TimeoutSeconds
		workload.Network = normalized.Network
		workload.Endpoints = normalized.Endpoints
		workload.Toolchains = normalized.Toolchains
	} else {
		workload.Network, err = jobs.NormalizeNetworkProfile(workload.Network)
		if err != nil || workload.Network != jobs.NetworkNone || len(workload.Endpoints) != 0 || len(workload.Toolchains) != 0 {
			return jobs.StartResult{}, fault.Error("legacy workload requests cannot carry network, endpoint, or toolchain intent")
		}
	}

	resolvedToolchains, err := l.resolveToolchains(ctx, workload.ID, workload.Toolchains)
	if err != nil {
		return jobs.StartResult{}, fault.Error("workload toolchain selection is unavailable")
	}
	discardResolvedToolchains := true
	defer func() {
		if discardResolvedToolchains && resolvedToolchains.Discard != nil {
			_ = resolvedToolchains.Discard()
		}
	}()

	network, err := sandboxNetworkProfile(workload.Network)
	if err != nil {
		return jobs.StartResult{}, fault.Error("workload request is not allowed")
	}
	endpoints := sandboxEndpointSpecs(workload.Endpoints)
	plan, err := l.policy.Plan(sandbox.WorkloadSpec{
		ID: workload.ID, PolicySHA256: policySHA256, CWD: workload.CWD,
		Argv: append([]string(nil), workload.Argv...), Network: network, Endpoints: endpoints,
		Toolchains: append([]sandbox.ToolchainMount(nil), resolvedToolchains.Mounts...),
		InputPath:  workload.InputPath, MaxOutputBytes: workload.MaxOutputBytes,
	})
	if err != nil || !launcherJobIDPattern.MatchString(workload.ID) {
		return jobs.StartResult{}, fault.Error("workload request is not allowed")
	}
	lifetime, err := jobs.ResolveRequestedTimeout(workload.TimeoutSeconds, l.timeout)
	if err != nil {
		return jobs.StartResult{}, fault.New(
			fault.CodeInvalidInput, "requested workload lifetime exceeds the trusted launcher limit",
			false, "reduce timeout_seconds and retry",
		)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return jobs.StartResult{}, errors.New("launcher is shutting down")
	}

	now := l.now()
	if err = l.journal.Prune(now); err != nil {
		return jobs.StartResult{}, fault.Error("launcher job admission failed")
	}
	_, retained, err := l.journal.Get(workload.ID)
	if err != nil {
		return jobs.StartResult{}, fault.Error("launcher job admission failed")
	}
	if !retained && len(l.active) >= l.maxConcurrentJobs {
		return jobs.StartResult{}, fault.New(
			fault.CodeQuotaExceeded,
			"launcher concurrent job capacity is full",
			true,
			"wait for an active job to finish or cancel one before retrying",
		)
	}
	deadline := now.Add(lifetime)
	var record jobs.Record
	var replayed bool
	if workload.RequestID != "" {
		record, replayed, err = l.journal.AdmitRequestWithToolchains(
			workload.ID, backendReference(plan), workload.RequestID, workload.RequestSHA256,
			workload.Network, workload.Endpoints, workload.Toolchains, deadline, now,
		)
		if errors.Is(err, jobs.ErrReplayConflict) {
			return jobs.StartResult{}, fault.New(
				fault.CodeConflict, jobs.ErrReplayConflict.Error(),
				false, "use the original start input or choose a new request_id",
			)
		}
	} else {
		if workload.Network != jobs.NetworkNone || len(workload.Endpoints) != 0 || len(workload.Toolchains) != 0 {
			return jobs.StartResult{}, fault.Error("legacy workload requests cannot carry network, endpoint, or toolchain intent")
		}
		if _, active := l.active[workload.ID]; active {
			return jobs.StartResult{}, fault.Error("workload ID is already registered")
		}
		record, err = l.journal.Admit(workload.ID, backendReference(plan), deadline, now)
	}
	if errors.Is(err, jobs.ErrJournalCapacity) {
		return jobs.StartResult{}, fault.New(
			fault.CodeQuotaExceeded,
			"launcher retained job-result capacity is full",
			true,
			"wait for retained job results to expire before retrying",
		)
	}
	if err != nil {
		return jobs.StartResult{}, fault.Error("launcher job admission failed")
	}
	if replayed {
		return startResultFromRecord(record, true), nil
	}
	if _, active := l.active[workload.ID]; active {
		return jobs.StartResult{}, errors.New("launcher job registry conflicts with durable admission")
	}

	jobCtx, cancel := context.WithDeadline(l.ctx, deadline)
	job := &ownedJob{
		cancel: cancel, done: make(chan struct{}), rawOutputLimit: workload.MaxOutputBytes,
		closeToolchains: resolvedToolchains.Close,
		toolchains:      append([]jobs.ToolchainRef(nil), workload.Toolchains...),
	}
	discardResolvedToolchains = false
	l.active[workload.ID] = job
	if workload.MaxOutputBytes > 0 {
		l.runs[workload.ID] = job
	}
	l.wg.Add(1)
	go l.runNew(workload.ID, plan, jobCtx, job)
	return startResultFromRecord(record, false), nil
}

func startResultFromRecord(record jobs.Record, replayed bool) jobs.StartResult {
	return jobs.StartResult{
		RequestID:  record.RequestID,
		JobID:      record.ID,
		State:      record.State,
		Replayed:   replayed,
		Detached:   true,
		DeadlineAt: record.DeadlineAt,
	}
}

func (l *lifecycle) runNew(id string, plan sandbox.Plan, ctx context.Context, job *ownedJob) {
	defer l.finish(id, job)
	started, startErr := l.runner.StartJob(ctx, plan)

	instanceBound := false
	if started.InstanceRef != "" {
		if _, bindErr := l.journal.BindInstance(id, started.InstanceRef, l.now()); bindErr != nil {
			result := jobs.Result{Outcome: jobs.OutcomeUnknown, Cleanup: jobs.CleanupFailed}
			var cleanupErr error
			if started.Created && started.Resource.Valid() {
				cleanup, err := l.runner.CleanupJob(context.Background(), started.Resource, started.InstanceRef)
				result.Cleanup = terminalCleanup(cleanup, err)
				cleanupErr = err
			}
			_, terminalErr := l.journal.MarkTerminal(id, result, l.now())
			job.err = errors.Join(bindErr, cleanupErr, terminalErr)
			return
		}
		instanceBound = true
	}

	if startErr != nil {
		outcome := jobs.OutcomeLaunchFailed
		if errors.Is(ctx.Err(), context.Canceled) {
			outcome = jobs.OutcomeCanceled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			outcome = jobs.OutcomeTimedOut
		}
		result := jobs.Result{Outcome: outcome, Cleanup: jobs.CleanupNotRequired}
		if started.Created {
			if !instanceBound || !started.Resource.Valid() {
				result.Cleanup = jobs.CleanupFailed
			} else {
				cleanup, cleanupErr := l.runner.CleanupJob(context.Background(), started.Resource, started.InstanceRef)
				result.Cleanup = terminalCleanup(cleanup, cleanupErr)
			}
		}
		_, persistErr := l.journal.MarkTerminal(id, result, l.now())
		if persistErr != nil {
			job.err = errors.Join(startErr, persistErr)
			return
		}
		if job.rawOutputLimit > 0 {
			raw := jobs.RunExecutionResult{Outcome: outcome, Cleanup: result.Cleanup}
			if !raw.Valid(job.rawOutputLimit) {
				job.err = errors.New("sandbox runner returned an invalid synchronous launch result")
				return
			}
			job.rawResult = &raw
		}
		return
	}

	if !instanceBound || !started.Resource.Valid() {
		result := jobs.Result{Outcome: jobs.OutcomeUnknown, Cleanup: jobs.CleanupFailed}
		if _, persistErr := l.journal.MarkTerminal(id, result, l.now()); persistErr != nil {
			job.err = errors.Join(errors.New("sandbox runner returned no durable instance identity"), persistErr)
		}
		return
	}

	if _, err := l.journal.BindEndpointLeases(id, jobEndpointLeases(started.EndpointBindings), l.now()); err != nil {
		cleanup, cleanupErr := l.runner.CleanupJob(context.Background(), started.Resource, started.InstanceRef)
		result := jobs.Result{Outcome: jobs.OutcomeUnknown, Cleanup: terminalCleanup(cleanup, cleanupErr)}
		if _, terminalErr := l.journal.MarkTerminal(id, result, l.now()); terminalErr != nil {
			job.err = errors.Join(err, cleanupErr, terminalErr)
		}
		return
	}

	if _, err := l.journal.MarkRunning(id, l.now()); err != nil {
		cleanup, cleanupErr := l.runner.CleanupJob(context.Background(), started.Resource, started.InstanceRef)
		result := jobs.Result{Outcome: jobs.OutcomeUnknown, Cleanup: terminalCleanup(cleanup, cleanupErr)}
		if _, terminalErr := l.journal.MarkTerminal(id, result, l.now()); terminalErr != nil {
			job.err = errors.Join(err, cleanupErr, terminalErr)
		}
		return
	}
	l.observeUntilTerminal(id, started.Resource, started.InstanceRef, ctx, job)
}

func (l *lifecycle) observeUntilTerminal(id string, resource sandbox.Resource, instanceRef string, ctx context.Context, job *ownedJob) {
	for {
		result, observeErr := l.runner.ObserveJob(ctx, resource, instanceRef)
		if terminalSandboxResult(result) {
			l.persistObservedTerminal(id, resource, instanceRef, result, job)
			return
		}
		if ctx.Err() != nil {
			cleanup, cleanupErr := l.runner.CleanupJob(context.Background(), resource, instanceRef)
			outcome := jobs.OutcomeCanceled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				outcome = jobs.OutcomeTimedOut
			}
			result := jobs.Result{Outcome: outcome, Cleanup: terminalCleanup(cleanup, cleanupErr)}
			if _, err := l.journal.MarkTerminal(id, result, l.now()); err != nil {
				job.err = errors.Join(observeErr, cleanupErr, err)
			}
			return
		}
		if observeErr == nil && result.Outcome != sandbox.OutcomeUnknown {
			l.persistObservedTerminal(id, resource, instanceRef, result, job)
			return
		}
		select {
		case <-ctx.Done():
			continue
		case <-time.After(reconcileRetryDelay):
		}
	}
}

func terminalSandboxResult(result sandbox.Result) bool {
	if result.ExitCodeKnown {
		return true
	}
	if (result.Cleanup == sandbox.CleanupComplete || result.Cleanup == sandbox.CleanupFailed) &&
		result.Outcome == sandbox.OutcomeUnknown {
		return true
	}
	switch result.Outcome {
	case sandbox.OutcomeCanceled, sandbox.OutcomeTimedOut, sandbox.OutcomeOOMKilled, sandbox.OutcomeExited, sandbox.OutcomeLaunchFailed:
		return true
	default:
		return false
	}
}

func (l *lifecycle) persistObservedTerminal(
	id string, resource sandbox.Resource, instanceRef string, result sandbox.Result, job *ownedJob,
) {
	var raw jobs.RunExecutionResult
	var rawErr error
	if job.rawOutputLimit > 0 {
		rawResult := result
		rawResult.Output, rawResult.OutputTruncated, rawErr = l.runner.OutputJobLimit(
			context.Background(), resource, instanceRef, job.rawOutputLimit,
		)
		if rawErr == nil {
			raw, rawErr = l.convertRunResult(rawResult, job.rawOutputLimit)
		}
	}
	cleanup, persistErr := l.persistTerminal(id, resource, instanceRef, result)
	if rawErr == nil && job.rawOutputLimit > 0 {
		raw.Cleanup = cleanup
		if !raw.Valid(job.rawOutputLimit) {
			rawErr = errors.New("sandbox runner returned an invalid synchronous result")
		} else {
			job.rawResult = &raw
		}
	}
	job.err = errors.Join(rawErr, persistErr)
}

func (l *lifecycle) persistTerminal(
	id string, resource sandbox.Resource, instanceRef string, result sandbox.Result,
) (jobs.CleanupStatus, error) {
	converted, err := l.convertResult(result)
	if err != nil {
		return jobs.CleanupFailed, err
	}
	if _, err = l.journal.MarkTerminal(id, converted, l.now()); err != nil {
		return jobs.CleanupFailed, err
	}
	if converted.Cleanup != jobs.CleanupPending {
		return converted.Cleanup, nil
	}
	cleanup, cleanupErr := l.runner.CleanupJob(context.Background(), resource, instanceRef)
	status := terminalCleanup(cleanup, cleanupErr)
	_, persistErr := l.journal.MarkCleanup(id, status, l.now())
	return status, errors.Join(cleanupErr, persistErr)
}

func (l *lifecycle) convertRunResult(result sandbox.Result, maximum int) (jobs.RunExecutionResult, error) {
	converted := jobs.RunExecutionResult{
		Outcome: mapOutcome(result.Outcome), Output: append([]byte(nil), result.Output...),
		Truncated: result.OutputTruncated, Cleanup: jobCleanup(result.Cleanup),
	}
	if result.ExitCodeKnown {
		exitCode := result.ExitCode
		converted.ExitCode = &exitCode
	}
	if !converted.Valid(maximum) {
		return jobs.RunExecutionResult{}, errors.New("sandbox runner returned an invalid synchronous result")
	}
	return converted, nil
}

func (l *lifecycle) convertResult(result sandbox.Result) (jobs.Result, error) {
	output, err := jobs.NormalizeOutput(result.Output, l.journal.MaxOutputBytes(), result.OutputTruncated)
	if err != nil {
		return jobs.Result{}, err
	}
	converted := jobs.Result{
		Outcome: mapOutcome(result.Outcome),
		Output:  output,
		Cleanup: jobCleanup(result.Cleanup),
	}
	if result.ExitCodeKnown {
		exitCode := result.ExitCode
		converted.ExitCode = &exitCode
	}
	if !converted.Outcome.Valid() || !converted.Cleanup.Valid() {
		return jobs.Result{}, errors.New("sandbox runner returned an invalid result")
	}
	return converted, nil
}

func mapOutcome(value sandbox.Outcome) jobs.Outcome {
	switch value {
	case sandbox.OutcomeExited:
		return jobs.OutcomeExited
	case sandbox.OutcomeCanceled:
		return jobs.OutcomeCanceled
	case sandbox.OutcomeTimedOut:
		return jobs.OutcomeTimedOut
	case sandbox.OutcomeOOMKilled:
		return jobs.OutcomeOOMKilled
	case sandbox.OutcomeLaunchFailed:
		return jobs.OutcomeLaunchFailed
	case sandbox.OutcomeUnknown:
		return jobs.OutcomeUnknown
	default:
		return ""
	}
}

func jobCleanup(value sandbox.CleanupStatus) jobs.CleanupStatus {
	switch value {
	case sandbox.CleanupPending:
		return jobs.CleanupPending
	case sandbox.CleanupComplete:
		return jobs.CleanupComplete
	case sandbox.CleanupFailed:
		return jobs.CleanupFailed
	case sandbox.CleanupNotRequired:
		return jobs.CleanupNotRequired
	default:
		return ""
	}
}

func terminalCleanup(value sandbox.CleanupStatus, err error) jobs.CleanupStatus {
	status := jobCleanup(value)
	if err != nil || (status != jobs.CleanupComplete && status != jobs.CleanupNotRequired) {
		return jobs.CleanupFailed
	}
	return status
}

func backendReference(plan sandbox.Plan) string {
	return "oci:" + plan.PolicySHA256() + ":" + plan.SandboxSHA256()
}

func resourceFromRecord(record jobs.Record) (sandbox.Resource, error) {
	digests, ok := strings.CutPrefix(record.BackendRef, "oci:")
	if !ok {
		return sandbox.Resource{}, errors.New("job record backend is unsupported")
	}
	policySHA256, sandboxSHA256, ok := strings.Cut(digests, ":")
	if !ok {
		return sandbox.Resource{}, errors.New("job record backend is unsupported")
	}
	return sandbox.NewResource(record.ID, policySHA256, sandboxSHA256)
}

func endpointSpecsFromRecord(record jobs.Record) []sandbox.EndpointSpec {
	result := make([]sandbox.EndpointSpec, len(record.EndpointRequests))
	for index, endpoint := range record.EndpointRequests {
		result[index] = sandbox.EndpointSpec{Name: endpoint.Name, Port: endpoint.Port}
	}
	return result
}

func (l *lifecycle) bindRecoveredEndpointLeases(
	record jobs.Record, resource sandbox.Resource,
) (jobs.Record, error) {
	bindings, err := l.runner.EndpointBindings(
		l.ctx, resource, record.InstanceRef, endpointSpecsFromRecord(record),
	)
	if err != nil {
		return jobs.Record{}, err
	}
	return l.journal.BindEndpointLeases(record.ID, jobEndpointLeases(bindings), l.now())
}

func (l *lifecycle) terminateRecoveredEndpointAuthority(
	record jobs.Record, resource sandbox.Resource,
) error {
	if _, err := l.journal.MarkTerminal(record.ID, jobs.Result{
		Outcome: jobs.OutcomeUnknown, Cleanup: jobs.CleanupPending,
	}, l.now()); err != nil {
		return err
	}
	cleanup, cleanupErr := l.runner.CleanupJob(context.Background(), resource, record.InstanceRef)
	status := terminalCleanup(cleanup, cleanupErr)
	_, markErr := l.journal.MarkCleanup(record.ID, status, l.now())
	l.removeRunInput(record.ID)
	return markErr
}

func (l *lifecycle) reconcileRunInputs(records []jobs.Record) error {
	if l.runInputDirectory == "" {
		return nil
	}
	entries, err := os.ReadDir(l.runInputDirectory)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, record := range records {
		if record.State != jobs.StateTerminal {
			keep[record.ID] = true
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		id := strings.TrimSuffix(name, ".stdin")
		if entry.IsDir() || name == id || !launcherJobIDPattern.MatchString(id) || keep[id] {
			continue
		}
		if err = os.Remove(filepath.Join(l.runInputDirectory, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (l *lifecycle) reconcile() error {
	records, err := l.journal.List()
	if err != nil {
		return err
	}
	if err = l.reconcileRunInputs(records); err != nil {
		return err
	}
	for _, record := range records {
		resource, resourceErr := resourceFromRecord(record)
		if resourceErr != nil {
			return resourceErr
		}
		if record.State == jobs.StateTerminal {
			if record.Result != nil &&
				(record.Result.Cleanup == jobs.CleanupPending || record.Result.Cleanup == jobs.CleanupFailed) &&
				record.InstanceRef != "" {
				cleanup, cleanupErr := l.runner.CleanupJob(context.Background(), resource, record.InstanceRef)
				status := terminalCleanup(cleanup, cleanupErr)
				if _, markErr := l.journal.MarkCleanup(record.ID, status, l.now()); markErr != nil {
					return errors.Join(cleanupErr, markErr)
				}
			}
			continue
		}

		if record.InstanceRef == "" {
			state, inspectErr := l.runner.Inspect(l.ctx, resource)
			if inspectErr != nil {
				return errors.New("launcher could not reconcile an unbound workload")
			}
			cleanup := jobs.CleanupComplete
			if state.Exists {
				cleanup = jobs.CleanupFailed
			}
			if _, err = l.journal.MarkTerminal(record.ID, jobs.Result{
				Outcome: jobs.OutcomeUnknown, Cleanup: cleanup,
			}, l.now()); err != nil {
				return err
			}
			l.removeRunInput(record.ID)
			continue
		}

		state, inspectErr := l.runner.InspectJob(l.ctx, resource, record.InstanceRef)
		if errors.Is(inspectErr, sandbox.ErrInstanceMismatch) {
			if _, err = l.journal.MarkTerminal(record.ID, jobs.Result{
				Outcome: jobs.OutcomeUnknown, Cleanup: jobs.CleanupFailed,
			}, l.now()); err != nil {
				return err
			}
			l.removeRunInput(record.ID)
			continue
		}
		if inspectErr != nil {
			return errors.New("launcher could not reconcile an existing workload")
		}
		if !state.Exists {
			if _, err = l.journal.MarkTerminal(record.ID, jobs.Result{
				Outcome: jobs.OutcomeUnknown, Cleanup: jobs.CleanupComplete,
			}, l.now()); err != nil {
				return err
			}
			l.removeRunInput(record.ID)
			continue
		}
		if !state.Running && !state.Terminal {
			if _, err = l.journal.MarkTerminal(record.ID, jobs.Result{
				Outcome: jobs.OutcomeUnknown, Cleanup: jobs.CleanupPending,
			}, l.now()); err != nil {
				return err
			}
			cleanup, cleanupErr := l.runner.CleanupJob(context.Background(), resource, record.InstanceRef)
			status := terminalCleanup(cleanup, cleanupErr)
			if _, err = l.journal.MarkCleanup(record.ID, status, l.now()); err != nil {
				return errors.Join(cleanupErr, err)
			}
			l.removeRunInput(record.ID)
			continue
		}

		recoveredRecord, bindErr := l.bindRecoveredEndpointLeases(record, resource)
		if bindErr != nil {
			if terminateErr := l.terminateRecoveredEndpointAuthority(record, resource); terminateErr != nil {
				return errors.Join(bindErr, terminateErr)
			}
			continue
		}
		record = recoveredRecord
		if record.State == jobs.StateAdmitted {
			record, err = l.journal.MarkRunning(record.ID, l.now())
			if err != nil {
				return err
			}
		}
		if err = l.spawnRecovered(record, resource); err != nil {
			return err
		}
	}
	return l.reconcileTerminalToolchains()
}

func (l *lifecycle) cleanupTerminalToolchains(id string, fallback []jobs.ToolchainRef) error {
	if len(fallback) == 0 {
		return nil
	}
	if l.toolchains == nil {
		return errors.New("launcher toolchain cleanup is not configured")
	}
	record, ok, err := l.journal.Get(id)
	if err != nil || !ok || record.State != jobs.StateTerminal || record.Result == nil {
		return err
	}
	if record.Result.Cleanup != jobs.CleanupComplete && record.Result.Cleanup != jobs.CleanupNotRequired {
		return nil
	}
	refs := record.Toolchains
	if len(refs) == 0 {
		refs = fallback
	}
	return l.toolchains.Cleanup(id, refs)
}

func (l *lifecycle) reconcileTerminalToolchains() error {
	records, err := l.journal.List()
	if err != nil {
		return err
	}
	for _, record := range records {
		if len(record.Toolchains) == 0 || record.State != jobs.StateTerminal || record.Result == nil ||
			(record.Result.Cleanup != jobs.CleanupComplete && record.Result.Cleanup != jobs.CleanupNotRequired) {
			continue
		}
		if l.toolchains == nil {
			return errors.New("launcher toolchain cleanup is not configured")
		}
		if err = l.toolchains.Cleanup(record.ID, record.Toolchains); err != nil {
			return err
		}
	}
	return nil
}

func (l *lifecycle) spawnRecovered(record jobs.Record, resource sandbox.Resource) error {
	deadline, err := time.Parse(time.RFC3339Nano, record.DeadlineAt)
	if err != nil {
		return errors.New("job record deadline is invalid")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("launcher is shutting down")
	}
	if _, exists := l.active[record.ID]; exists {
		return errors.New("recovered job is already active")
	}
	jobCtx, cancel := context.WithDeadline(l.ctx, deadline)
	resolvedToolchains, err := l.resolveToolchains(jobCtx, record.ID, record.Toolchains)
	if err != nil {
		cancel()
		return errors.New("recovered job toolchain selection is unavailable")
	}
	job := &ownedJob{
		cancel: cancel, done: make(chan struct{}),
		closeToolchains: resolvedToolchains.Close,
		toolchains:      append([]jobs.ToolchainRef(nil), record.Toolchains...),
	}
	l.active[record.ID] = job
	l.wg.Add(1)
	go func() {
		defer l.finish(record.ID, job)
		l.observeUntilTerminal(record.ID, resource, record.InstanceRef, jobCtx, job)
	}()
	return nil
}

func (l *lifecycle) finish(id string, job *ownedJob) {
	job.cancel()
	l.removeRunInput(id)
	if job.closeToolchains != nil {
		job.err = errors.Join(job.err, job.closeToolchains())
		job.closeToolchains = nil
	}
	job.err = errors.Join(job.err, l.cleanupTerminalToolchains(id, job.toolchains))
	l.mu.Lock()
	if l.active[id] == job {
		delete(l.active, id)
	}
	close(job.done)
	l.mu.Unlock()
	l.wg.Done()
}

func (l *lifecycle) lookupActive(id string) (*ownedJob, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	job, ok := l.active[id]
	return job, ok
}

func (l *lifecycle) durableRecord(id string) (jobs.Record, error) {
	if !launcherJobIDPattern.MatchString(id) {
		return jobs.Record{}, fault.Error("workload is not registered")
	}
	if err := l.journal.Prune(l.now()); err != nil {
		return jobs.Record{}, err
	}
	record, ok, err := l.journal.Get(id)
	if err != nil {
		return jobs.Record{}, err
	}
	if !ok {
		return jobs.Record{}, fault.Error("workload is not registered")
	}
	return record, nil
}

func (l *lifecycle) inspect(id string) (jobs.Status, error) {
	record, err := l.durableRecord(id)
	if err != nil {
		return jobs.Status{}, err
	}
	return jobs.StatusFromRecord(record)
}

func (l *lifecycle) output(ctx context.Context, id string) (jobs.OutputSnapshot, error) {
	record, err := l.durableRecord(id)
	if err != nil {
		return jobs.OutputSnapshot{}, err
	}
	snapshot, err := jobs.OutputFromRecord(record)
	if err != nil {
		return jobs.OutputSnapshot{}, err
	}
	if record.State == jobs.StateTerminal || record.InstanceRef == "" {
		return snapshot, nil
	}
	resource, err := resourceFromRecord(record)
	if err != nil {
		return jobs.OutputSnapshot{}, err
	}
	raw, truncated, outputErr := l.runner.OutputJob(ctx, resource, record.InstanceRef)
	if outputErr != nil {
		latest, latestErr := l.durableRecord(id)
		if latestErr == nil && latest.State == jobs.StateTerminal {
			return jobs.OutputFromRecord(latest)
		}
		return jobs.OutputSnapshot{}, outputErr
	}
	output, err := jobs.NormalizeOutput(raw, l.journal.MaxOutputBytes(), truncated)
	if err != nil {
		return jobs.OutputSnapshot{}, err
	}
	snapshot.Output = output.Text
	snapshot.Truncated = output.Truncated

	latest, latestErr := l.durableRecord(id)
	if latestErr == nil && latest.State == jobs.StateTerminal {
		return jobs.OutputFromRecord(latest)
	}
	return snapshot, nil
}

func (l *lifecycle) wait(ctx context.Context, id string) (jobs.Result, error) {
	if !launcherJobIDPattern.MatchString(id) {
		return jobs.Result{}, fault.Error("workload is not registered")
	}
	if err := l.journal.Prune(l.now()); err != nil {
		return jobs.Result{}, err
	}
	record, ok, err := l.journal.Get(id)
	if err != nil {
		return jobs.Result{}, err
	}
	if !ok {
		return jobs.Result{}, fault.Error("workload is not registered")
	}
	if record.State == jobs.StateTerminal && record.Result != nil {
		if record.Result.Cleanup != jobs.CleanupPending {
			return *record.Result, nil
		}
		if job, active := l.lookupActive(id); active {
			return l.waitActive(ctx, id, job)
		}
		return *record.Result, nil
	}
	job, active := l.lookupActive(id)
	if !active {
		latest, latestErr := l.durableRecord(id)
		if latestErr == nil && latest.State == jobs.StateTerminal && latest.Result != nil {
			return *latest.Result, nil
		}
		return jobs.Result{}, fault.Error("workload recovery is pending")
	}
	return l.waitActive(ctx, id, job)
}

func (l *lifecycle) waitActive(ctx context.Context, id string, job *ownedJob) (jobs.Result, error) {
	select {
	case <-job.done:
		record, ok, err := l.journal.Get(id)
		if err != nil {
			return jobs.Result{}, err
		}
		if ok && record.State == jobs.StateTerminal && record.Result != nil {
			return *record.Result, nil
		}
		if job.err != nil {
			return jobs.Result{}, job.err
		}
		return jobs.Result{}, fault.Error("workload terminal state is unavailable")
	case <-ctx.Done():
		return jobs.Result{}, ctx.Err()
	}
}

func (l *lifecycle) cancelStatus(ctx context.Context, id string) (jobs.CancelResult, error) {
	record, err := l.durableRecord(id)
	if err != nil {
		return jobs.CancelResult{}, err
	}
	if record.State == jobs.StateTerminal {
		status, statusErr := jobs.StatusFromRecord(record)
		if statusErr != nil {
			return jobs.CancelResult{}, statusErr
		}
		return jobs.CancelResult{JobID: id, Canceled: false, Status: status}, nil
	}
	job, active := l.lookupActive(id)
	if !active {
		latest, latestErr := l.durableRecord(id)
		if latestErr == nil && latest.State == jobs.StateTerminal {
			status, statusErr := jobs.StatusFromRecord(latest)
			if statusErr != nil {
				return jobs.CancelResult{}, statusErr
			}
			return jobs.CancelResult{JobID: id, Canceled: false, Status: status}, nil
		}
		return jobs.CancelResult{}, fault.Error("workload recovery is pending")
	}
	job.cancel()
	select {
	case <-job.done:
		record, err = l.durableRecord(id)
		if err != nil {
			if job.err != nil {
				return jobs.CancelResult{}, job.err
			}
			return jobs.CancelResult{}, err
		}
		status, statusErr := jobs.StatusFromRecord(record)
		if statusErr != nil {
			return jobs.CancelResult{}, statusErr
		}
		return jobs.CancelResult{JobID: id, Canceled: true, Status: status}, nil
	case <-ctx.Done():
		return jobs.CancelResult{}, ctx.Err()
	}
}

func (l *lifecycle) cancelJob(ctx context.Context, id string) (bool, error) {
	result, err := l.cancelStatus(ctx, id)
	if err != nil {
		return false, err
	}
	if result.Status.Cleanup == jobs.CleanupFailed {
		return result.Canceled, fault.Error("workload cleanup is incomplete")
	}
	return result.Canceled, nil
}

func toWaitResult(result jobs.Result) waitResult {
	return waitResult{
		ExitCode:  result.ExitCode,
		Outcome:   result.Outcome,
		Output:    result.Output.Text,
		Truncated: result.Output.Truncated,
		Cleanup:   result.Cleanup,
	}
}

func (l *lifecycle) close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.cancel()
	for _, job := range l.active {
		job.cancel()
	}
	l.mu.Unlock()

	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(launcherStopTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errors.New("launcher jobs did not stop before shutdown timeout")
	}
}

func Run(ctx context.Context, options Options) error {
	if !filepath.IsAbs(options.Socket) || filepath.Clean(options.Socket) != options.Socket || options.SocketGID < 0 {
		return errors.New("launcher requires an absolute socket path and socket GID")
	}
	resolver := identity.FixedUIDResolver{UID: options.ExecutorUID, Kind: identity.Executor}
	if !resolver.Valid() {
		return errors.New("launcher requires a non-root executor UID")
	}
	lifecycle, err := newLifecycle(ctx, options.Policy, options.Runner, options.Toolchains, options.Journal, options.RunTimeout)
	if err != nil {
		return err
	}
	maxConcurrentJobs, err := normalizeMaxConcurrentJobs(options.MaxConcurrentJobs)
	if err != nil {
		return err
	}
	lifecycle.maxConcurrentJobs = maxConcurrentJobs
	defer lifecycle.close()
	if err = lifecycle.reconcile(); err != nil {
		return err
	}

	listener, err := daemon.Listen(options.Socket, options.SocketGID)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := rpc.Server{
		Limits: rpc.Limits{
			RequestBytes: jobs.MaxRunRequestBytes, ResponseBytes: jobs.MaxRunResultBytes,
		},
		Principals:     resolver,
		Operations:     lifecycle.operations(),
		MaxConnections: 16,
	}
	if options.Ready != nil {
		if err = options.Ready(); err != nil {
			return err
		}
	}
	err = server.Serve(ctx, listener)
	return errors.Join(err, lifecycle.close())
}
