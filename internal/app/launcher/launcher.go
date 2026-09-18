// Package launcher owns the narrow privileged workload-launch role.
package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/fault"
	"loki/internal/platform/sandbox"
	"loki/internal/rpc"
)

const (
	maxRunTimeout       = 24 * time.Hour
	minResultRetention  = 10 * time.Millisecond
	maxResultRetention  = time.Hour
	maxOwnedJobs        = 1024
	launcherStopTimeout = 30 * time.Second
)

var launcherJobIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Runner interface {
	Run(context.Context, sandbox.Plan) (sandbox.Result, error)
}

type Options struct {
	Socket          string
	SocketGID       int
	ExecutorUID     uint32
	Policy          sandbox.Policy
	Runner          Runner
	RunTimeout      time.Duration
	ResultRetention time.Duration
	MaxJobs         int
	Ready           func() error
}

type lifecycle struct {
	ctx       context.Context
	cancel    context.CancelFunc
	policy    sandbox.Policy
	runner    Runner
	timeout   time.Duration
	retention time.Duration
	maxJobs   int

	mu     sync.Mutex
	jobs   map[string]*ownedJob
	closed bool
	wg     sync.WaitGroup
}

type ownedJob struct {
	cancel context.CancelFunc
	done   chan struct{}
	result sandbox.Result
	err    error
	expiry *time.Timer
}

type startResult struct {
	ID string `json:"id"`
}

type waitRequest struct {
	ID string `json:"id"`
}

type waitResult struct {
	ExitCode int64 `json:"exit_code"`
}

type cancelResult struct {
	Canceled bool `json:"canceled"`
}

func newLifecycle(parent context.Context, policy sandbox.Policy, runner Runner, timeout, retention time.Duration, maxJobs int) (*lifecycle, error) {
	if !policy.Valid() {
		return nil, errors.New("launcher requires a valid sandbox policy")
	}
	if runner == nil {
		return nil, errors.New("launcher requires a sandbox runner")
	}
	if timeout < time.Second || timeout > maxRunTimeout {
		return nil, errors.New("launcher run timeout is outside the supported range")
	}
	if retention < minResultRetention || retention > maxResultRetention {
		return nil, errors.New("launcher result retention is outside the supported range")
	}
	if maxJobs < 1 || maxJobs > maxOwnedJobs {
		return nil, errors.New("launcher job capacity is outside the supported range")
	}
	ctx, cancel := context.WithCancel(parent)
	return &lifecycle{
		ctx:       ctx,
		cancel:    cancel,
		policy:    policy,
		runner:    runner,
		timeout:   timeout,
		retention: retention,
		maxJobs:   maxJobs,
		jobs:      map[string]*ownedJob{},
	}, nil
}

func (l *lifecycle) operations() map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"start": {
			Grant: controlpolicy.WorkloadLaunch,
			Handle: func(_ context.Context, raw json.RawMessage) (any, error) {
				spec, err := rpc.Decode[sandbox.WorkloadSpec](raw)
				if err != nil {
					return nil, err
				}
				if err = l.start(spec); err != nil {
					return nil, err
				}
				return startResult{ID: spec.ID}, nil
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
				return waitResult{ExitCode: result.ExitCode}, nil
			},
		},
		"cancel": {
			Grant: controlpolicy.WorkloadLaunch,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[waitRequest](raw)
				if err != nil {
					return nil, err
				}
				canceled, err := l.cancelJob(ctx, request.ID)
				if err != nil {
					return nil, err
				}
				return cancelResult{Canceled: canceled}, nil
			},
		},
	}
}

func (l *lifecycle) start(spec sandbox.WorkloadSpec) error {
	plan, err := l.policy.Plan(spec)
	if err != nil {
		return fault.Error("workload request is not allowed")
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return errors.New("launcher is shutting down")
	}
	if _, exists := l.jobs[spec.ID]; exists {
		l.mu.Unlock()
		return fault.Error("workload ID is already registered")
	}
	if len(l.jobs) >= l.maxJobs {
		l.mu.Unlock()
		return fault.Error("launcher job capacity is exhausted")
	}
	jobCtx, cancel := context.WithTimeout(l.ctx, l.timeout)
	job := &ownedJob{cancel: cancel, done: make(chan struct{})}
	l.jobs[spec.ID] = job
	l.wg.Add(1)
	l.mu.Unlock()

	go func() {
		defer l.wg.Done()
		result, runErr := l.runner.Run(jobCtx, plan)
		cancel()

		l.mu.Lock()
		job.result = result
		job.err = runErr
		close(job.done)
		if current := l.jobs[spec.ID]; current == job && !l.closed {
			job.expiry = time.AfterFunc(l.retention, func() {
				l.expire(spec.ID, job)
			})
		}
		l.mu.Unlock()
	}()
	return nil
}

func (l *lifecycle) expire(id string, job *ownedJob) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.jobs[id] == job {
		delete(l.jobs, id)
	}
}

func (l *lifecycle) lookup(id string) (*ownedJob, bool) {
	if !launcherJobIDPattern.MatchString(id) {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	job, ok := l.jobs[id]
	return job, ok
}

func (l *lifecycle) consume(id string, job *ownedJob) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.jobs[id] != job {
		return
	}
	delete(l.jobs, id)
	if job.expiry != nil {
		job.expiry.Stop()
	}
}

func (l *lifecycle) wait(ctx context.Context, id string) (sandbox.Result, error) {
	job, ok := l.lookup(id)
	if !ok {
		return sandbox.Result{}, fault.Error("workload is not registered")
	}
	select {
	case <-job.done:
		l.consume(id, job)
		if job.err != nil {
			return sandbox.Result{}, job.err
		}
		if job.result.ExitCode < 0 || job.result.ExitCode > 255 {
			return sandbox.Result{}, errors.New("sandbox runner returned an invalid exit code")
		}
		return job.result, nil
	case <-ctx.Done():
		return sandbox.Result{}, ctx.Err()
	}
}

func (l *lifecycle) cancelJob(ctx context.Context, id string) (bool, error) {
	job, ok := l.lookup(id)
	if !ok {
		return false, nil
	}
	select {
	case <-job.done:
		l.consume(id, job)
		if job.err != nil && !cancellationOnly(job.err) {
			return false, job.err
		}
		return false, nil
	default:
	}

	job.cancel()
	select {
	case <-job.done:
		l.consume(id, job)
		if job.err != nil && !cancellationOnly(job.err) {
			return true, job.err
		}
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

func cancellationOnly(err error) bool {
	if err == nil {
		return true
	}
	type unwrapMany interface{ Unwrap() []error }
	if many, ok := err.(unwrapMany); ok {
		children := many.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !cancellationOnly(child) {
				return false
			}
		}
		return true
	}
	type unwrapOne interface{ Unwrap() error }
	if one, ok := err.(unwrapOne); ok {
		if child := one.Unwrap(); child != nil {
			return cancellationOnly(child)
		}
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (l *lifecycle) close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.cancel()
	for _, job := range l.jobs {
		job.cancel()
		if job.expiry != nil {
			job.expiry.Stop()
		}
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
	case <-timer.C:
		return errors.New("launcher jobs did not stop before shutdown timeout")
	}

	l.mu.Lock()
	l.jobs = map[string]*ownedJob{}
	l.mu.Unlock()
	return nil
}

func Run(ctx context.Context, options Options) error {
	if !filepath.IsAbs(options.Socket) || filepath.Clean(options.Socket) != options.Socket || options.SocketGID < 0 {
		return errors.New("launcher requires an absolute socket path and socket GID")
	}
	resolver := identity.FixedUIDResolver{UID: options.ExecutorUID, Kind: identity.Executor}
	if !resolver.Valid() {
		return errors.New("launcher requires a non-root executor UID")
	}
	lifecycle, err := newLifecycle(ctx, options.Policy, options.Runner, options.RunTimeout, options.ResultRetention, options.MaxJobs)
	if err != nil {
		return err
	}
	defer lifecycle.close()

	listener, err := daemon.Listen(options.Socket, options.SocketGID)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := rpc.Server{
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
