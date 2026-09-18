// Package launcher owns the narrow privileged workload-launch role.
package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/fault"
	"loki/internal/platform/sandbox"
	"loki/internal/rpc"
)

const maxRunTimeout = 24 * time.Hour

type Runner interface {
	Run(context.Context, sandbox.Plan) (sandbox.Result, error)
}

type Options struct {
	Socket      string
	SocketGID   int
	ExecutorUID uint32
	Policy      sandbox.Policy
	Runner      Runner
	RunTimeout  time.Duration
	Ready       func() error
}

type runResult struct {
	ExitCode int64 `json:"exit_code"`
}

func Operations(policy sandbox.Policy, runner Runner, timeout time.Duration) (map[string]rpc.Operation, error) {
	if !policy.Valid() {
		return nil, errors.New("launcher requires a valid sandbox policy")
	}
	if runner == nil {
		return nil, errors.New("launcher requires a sandbox runner")
	}
	if timeout < time.Second || timeout > maxRunTimeout {
		return nil, errors.New("launcher run timeout is outside the supported range")
	}
	return map[string]rpc.Operation{
		"run": {
			Grant:   controlpolicy.WorkloadLaunch,
			Timeout: timeout,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				spec, err := rpc.Decode[sandbox.WorkloadSpec](raw)
				if err != nil {
					return nil, err
				}
				plan, err := policy.Plan(spec)
				if err != nil {
					return nil, fault.Error("workload request is not allowed")
				}
				result, err := runner.Run(ctx, plan)
				if err != nil {
					return nil, err
				}
				return runResult{ExitCode: result.ExitCode}, nil
			},
		},
	}, nil
}

func Run(ctx context.Context, options Options) error {
	if !filepath.IsAbs(options.Socket) || filepath.Clean(options.Socket) != options.Socket || options.SocketGID < 0 {
		return errors.New("launcher requires an absolute socket path and socket GID")
	}
	resolver := identity.FixedUIDResolver{UID: options.ExecutorUID, Kind: identity.Executor}
	if !resolver.Valid() {
		return errors.New("launcher requires a non-root executor UID")
	}
	operations, err := Operations(options.Policy, options.Runner, options.RunTimeout)
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
		MaxConnections: 16,
	}
	if options.Ready != nil {
		if err = options.Ready(); err != nil {
			return err
		}
	}
	return server.Serve(ctx, listener)
}
