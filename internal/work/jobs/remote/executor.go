package remote

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"loki/internal/rpc"
	"loki/internal/work/jobs"
)

type ExecutorOptions struct {
	Socket      string
	ExpectedUID *uint32
	Timeout     time.Duration
}

type Executor struct {
	client rpc.Client
}

func NewExecutor(options ExecutorOptions) (*Executor, error) {
	if !filepath.IsAbs(options.Socket) || filepath.Clean(options.Socket) != options.Socket ||
		options.Socket == string(filepath.Separator) || strings.ContainsRune(options.Socket, 0) {
		return nil, errors.New("remote executor requires an absolute clean socket path")
	}
	if options.ExpectedUID == nil {
		return nil, errors.New("remote executor requires an expected executor UID")
	}
	if options.Timeout < time.Second || options.Timeout > maxRunTimeout {
		return nil, errors.New("remote executor timeout is outside the supported range")
	}
	expectedUID := *options.ExpectedUID
	return &Executor{client: rpc.Client{
		Socket:      options.Socket,
		ExpectedUID: &expectedUID,
		Limits:      rpc.Limits{Timeout: options.Timeout},
	}}, nil
}

func (e *Executor) Start(ctx context.Context, request jobs.StartRequest) (jobs.StartResult, error) {
	if e == nil {
		return jobs.StartResult{}, errors.New("remote executor is not configured")
	}
	normalizedRequestID, err := jobs.NormalizeRequestID(request.RequestID)
	if err != nil {
		return jobs.StartResult{}, err
	}
	raw, err := e.client.Call(ctx, struct {
		Operation      string                 `json:"operation"`
		RequestID      string                 `json:"request_id"`
		CWD            string                 `json:"cwd,omitempty"`
		Argv           []string               `json:"argv"`
		TimeoutSeconds int                    `json:"timeout_seconds,omitempty"`
		Network        jobs.NetworkProfile    `json:"network,omitempty"`
		Endpoints      []jobs.EndpointRequest `json:"endpoints,omitempty"`
	}{
		Operation:      "start",
		RequestID:      request.RequestID,
		CWD:            request.CWD,
		Argv:           append([]string(nil), request.Argv...),
		TimeoutSeconds: request.TimeoutSeconds,
		Network:        request.Network,
		Endpoints:      append([]jobs.EndpointRequest(nil), request.Endpoints...),
	})
	if err != nil {
		return jobs.StartResult{}, err
	}
	var result jobs.StartResult
	if err = decodeStrict(raw, &result); err != nil {
		return jobs.StartResult{}, errors.New("executor returned an invalid start result")
	}
	expectedJobID, err := jobs.JobIDForRequestID(normalizedRequestID)
	if err != nil || result.RequestID != normalizedRequestID || result.JobID != expectedJobID ||
		!result.State.Valid() || !result.Detached {
		return jobs.StartResult{}, errors.New("executor returned an invalid start result")
	}
	deadline, err := time.Parse(time.RFC3339Nano, result.DeadlineAt)
	if err != nil || deadline.Location() != time.UTC {
		return jobs.StartResult{}, errors.New("executor returned an invalid start deadline")
	}
	return result, nil
}

func (e *Executor) Inspect(ctx context.Context, id string) (jobs.Status, error) {
	if e == nil {
		return jobs.Status{}, errors.New("remote executor is not configured")
	}
	if err := jobs.ValidateJobID(id); err != nil {
		return jobs.Status{}, err
	}
	raw, err := e.client.Call(ctx, struct {
		Operation string `json:"operation"`
		JobID     string `json:"job_id"`
	}{Operation: "inspect", JobID: id})
	if err != nil {
		return jobs.Status{}, err
	}
	var result jobs.Status
	if err = decodeStrict(raw, &result); err != nil || result.JobID != id || !result.State.Valid() {
		return jobs.Status{}, errors.New("executor returned an invalid inspect result")
	}
	return result, nil
}

func (e *Executor) Output(ctx context.Context, id string) (jobs.OutputSnapshot, error) {
	if e == nil {
		return jobs.OutputSnapshot{}, errors.New("remote executor is not configured")
	}
	if err := jobs.ValidateJobID(id); err != nil {
		return jobs.OutputSnapshot{}, err
	}
	raw, err := e.client.Call(ctx, struct {
		Operation string `json:"operation"`
		JobID     string `json:"job_id"`
	}{Operation: "output", JobID: id})
	if err != nil {
		return jobs.OutputSnapshot{}, err
	}
	var result jobs.OutputSnapshot
	if err = decodeStrict(raw, &result); err != nil || result.JobID != id ||
		!result.State.Valid() || len(result.Output) > jobs.MaxOutputBytes {
		return jobs.OutputSnapshot{}, errors.New("executor returned an invalid output result")
	}
	return result, nil
}

func (e *Executor) Cancel(ctx context.Context, id string) (jobs.CancelResult, error) {
	if e == nil {
		return jobs.CancelResult{}, errors.New("remote executor is not configured")
	}
	if err := jobs.ValidateJobID(id); err != nil {
		return jobs.CancelResult{}, err
	}
	raw, err := e.client.Call(ctx, struct {
		Operation string `json:"operation"`
		JobID     string `json:"job_id"`
	}{Operation: "cancel", JobID: id})
	if err != nil {
		return jobs.CancelResult{}, err
	}
	var result jobs.CancelResult
	if err = decodeStrict(raw, &result); err != nil || result.JobID != id ||
		result.Status.JobID != id || !result.Status.State.Valid() {
		return jobs.CancelResult{}, errors.New("executor returned an invalid cancel result")
	}
	return result, nil
}

var _ jobs.Controller = (*Executor)(nil)
