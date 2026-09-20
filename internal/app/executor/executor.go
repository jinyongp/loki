// Package executor owns the unprivileged internal job-execution role.
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/rpc"
	"loki/internal/work/jobs"
)

const maxRunTimeout = 24 * time.Hour

type Runner interface {
	jobs.Controller
	Run(context.Context, jobs.RunRequest) (jobs.RunResult, error)
}

type Options struct {
	Socket     string
	SocketGID  int
	AgentUID   uint32
	Runner     Runner
	RunTimeout time.Duration
	Ready      func() error
}

type jobRequest struct {
	JobID string `json:"job_id"`
}

func Operations(runner Runner, timeout time.Duration) (map[string]rpc.Operation, error) {
	if runner == nil {
		return nil, errors.New("executor requires a job runner")
	}
	if timeout < time.Second || timeout > maxRunTimeout {
		return nil, errors.New("executor run timeout is outside the supported range")
	}
	controlTimeout := min(timeout, 30*time.Second)
	return map[string]rpc.Operation{
		"run": {
			Grant:   controlpolicy.Agent,
			Timeout: timeout,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[jobs.RunRequest](raw)
				if err != nil {
					return nil, err
				}
				return runner.Run(ctx, request)
			},
		},
		"start": {
			Grant:   controlpolicy.Agent,
			Timeout: controlTimeout,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[jobs.StartRequest](raw)
				if err != nil {
					return nil, err
				}
				return runner.Start(ctx, request)
			},
		},
		"inspect": {
			Grant:   controlpolicy.Agent,
			Timeout: controlTimeout,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[jobRequest](raw)
				if err != nil {
					return nil, err
				}
				return runner.Inspect(ctx, request.JobID)
			},
		},
		"output": {
			Grant:   controlpolicy.Agent,
			Timeout: controlTimeout,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[jobRequest](raw)
				if err != nil {
					return nil, err
				}
				return runner.Output(ctx, request.JobID)
			},
		},
		"cancel": {
			Grant:   controlpolicy.Agent,
			Timeout: controlTimeout,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[jobRequest](raw)
				if err != nil {
					return nil, err
				}
				return runner.Cancel(ctx, request.JobID)
			},
		},
	}, nil
}

func Run(ctx context.Context, options Options) error {
	if !filepath.IsAbs(options.Socket) || filepath.Clean(options.Socket) != options.Socket ||
		options.Socket == string(filepath.Separator) || options.SocketGID < 0 {
		return errors.New("executor requires an absolute clean socket path and socket GID")
	}
	resolver := identity.FixedUIDResolver{UID: options.AgentUID, Kind: identity.Agent}
	if !resolver.Valid() {
		return errors.New("executor requires a non-root agent UID")
	}
	operations, err := Operations(options.Runner, options.RunTimeout)
	if err != nil {
		return err
	}
	listener, err := daemon.Listen(options.Socket, options.SocketGID)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := rpc.Server{
		Principals:     resolver,
		Operations:     operations,
		MaxConnections: 32,
	}
	if options.Ready != nil {
		if err = options.Ready(); err != nil {
			return err
		}
	}
	return server.Serve(ctx, listener)
}
