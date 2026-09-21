package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"loki/internal/rpc"
	"loki/internal/work/jobs"
)

const (
	maxRunTimeout         = 24 * time.Hour
	defaultCleanupTimeout = 10 * time.Second
)

var (
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	jobIDPattern  = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type Options struct {
	Socket       string
	ExpectedUID  *uint32
	PolicySHA256 string
	Timeout      time.Duration
}

type Launcher struct {
	client         rpc.Client
	policySHA256   string
	cleanupTimeout time.Duration
}

type waitResult struct {
	ExitCode  *int64             `json:"exit_code,omitempty"`
	Outcome   jobs.Outcome       `json:"outcome"`
	Output    string             `json:"output"`
	Truncated bool               `json:"truncated"`
	Cleanup   jobs.CleanupStatus `json:"cleanup"`
}

func New(options Options) (*Launcher, error) {
	if !filepath.IsAbs(options.Socket) || filepath.Clean(options.Socket) != options.Socket ||
		options.Socket == string(filepath.Separator) || strings.ContainsRune(options.Socket, 0) {
		return nil, errors.New("remote launcher requires an absolute clean socket path")
	}
	if options.ExpectedUID == nil {
		return nil, errors.New("remote launcher requires an expected launcher UID")
	}
	if !digestPattern.MatchString(options.PolicySHA256) {
		return nil, errors.New("remote launcher requires a valid policy digest")
	}
	if options.Timeout < time.Second || options.Timeout > maxRunTimeout {
		return nil, errors.New("remote launcher timeout is outside the supported range")
	}
	cleanupTimeout := min(options.Timeout, defaultCleanupTimeout)
	expectedUID := *options.ExpectedUID
	return &Launcher{
		client: rpc.Client{
			Socket:      options.Socket,
			ExpectedUID: &expectedUID,
			Limits: rpc.Limits{
				RequestBytes: jobs.MaxRunRequestBytes, ResponseBytes: jobs.MaxRunResultBytes,
				Timeout: options.Timeout,
			},
		},
		policySHA256:   options.PolicySHA256,
		cleanupTimeout: cleanupTimeout,
	}, nil
}

func (l *Launcher) Run(ctx context.Context, workload jobs.Workload) (jobs.RunExecutionResult, error) {
	if l == nil || !digestPattern.MatchString(l.policySHA256) || !jobIDPattern.MatchString(workload.ID) {
		return jobs.RunExecutionResult{}, errors.New("remote launcher is not configured")
	}
	raw, err := l.client.Call(ctx, struct {
		Operation      string   `json:"operation"`
		ID             string   `json:"id"`
		PolicySHA256   string   `json:"policy_sha256"`
		CWD            string   `json:"cwd"`
		Argv           []string `json:"argv"`
		Input          []byte   `json:"input,omitempty"`
		MaxOutputBytes int      `json:"max_output_bytes"`
	}{
		Operation: "run", ID: workload.ID, PolicySHA256: l.policySHA256,
		CWD: workload.CWD, Argv: append([]string(nil), workload.Argv...),
		Input: append([]byte(nil), workload.Input...), MaxOutputBytes: workload.MaxOutputBytes,
	})
	if err != nil {
		return jobs.RunExecutionResult{}, err
	}
	var result jobs.RunExecutionResult
	if err = decodeStrict(raw, &result); err != nil || !result.Valid(workload.MaxOutputBytes) {
		return jobs.RunExecutionResult{}, errors.New("launcher returned an invalid run result")
	}
	return result, nil
}

func (l *Launcher) Start(ctx context.Context, workload jobs.Workload) (jobs.StartResult, error) {
	if l == nil || !digestPattern.MatchString(l.policySHA256) || !jobIDPattern.MatchString(workload.ID) {
		return jobs.StartResult{}, errors.New("remote launcher is not configured")
	}
	request := struct {
		Operation      string                 `json:"operation"`
		ID             string                 `json:"id"`
		RequestID      string                 `json:"request_id,omitempty"`
		RequestSHA256  string                 `json:"request_sha256,omitempty"`
		PolicySHA256   string                 `json:"policy_sha256"`
		CWD            string                 `json:"cwd"`
		Argv           []string               `json:"argv"`
		TimeoutSeconds int                    `json:"timeout_seconds,omitempty"`
		Network        jobs.NetworkProfile    `json:"network,omitempty"`
		Endpoints      []jobs.EndpointRequest `json:"endpoints,omitempty"`
	}{
		Operation:      "start",
		ID:             workload.ID,
		RequestID:      workload.RequestID,
		RequestSHA256:  workload.RequestSHA256,
		PolicySHA256:   l.policySHA256,
		CWD:            workload.CWD,
		Argv:           append([]string(nil), workload.Argv...),
		TimeoutSeconds: workload.TimeoutSeconds,
		Network:        workload.Network,
		Endpoints:      append([]jobs.EndpointRequest(nil), workload.Endpoints...),
	}
	raw, err := l.client.Call(ctx, request)
	if err != nil {
		return jobs.StartResult{}, err
	}
	var result jobs.StartResult
	if err = decodeStrict(raw, &result); err != nil ||
		result.JobID != workload.ID || result.RequestID != workload.RequestID ||
		!result.State.Valid() || !result.Detached {
		return jobs.StartResult{}, errors.New("launcher returned an invalid start result")
	}
	if deadline, parseErr := time.Parse(time.RFC3339Nano, result.DeadlineAt); parseErr != nil || deadline.Location() != time.UTC {
		return jobs.StartResult{}, errors.New("launcher returned an invalid start deadline")
	}
	return result, nil
}

func (l *Launcher) Inspect(ctx context.Context, id string) (jobs.Status, error) {
	if l == nil || !jobIDPattern.MatchString(id) {
		return jobs.Status{}, errors.New("remote launcher is not configured")
	}
	raw, err := l.client.Call(ctx, struct {
		Operation string `json:"operation"`
		ID        string `json:"id"`
	}{Operation: "inspect", ID: id})
	if err != nil {
		return jobs.Status{}, err
	}
	var result jobs.Status
	if err = decodeStrict(raw, &result); err != nil || result.JobID != id || !result.State.Valid() {
		return jobs.Status{}, errors.New("launcher returned an invalid inspect result")
	}
	return result, nil
}

func (l *Launcher) Output(ctx context.Context, id string) (jobs.OutputSnapshot, error) {
	if l == nil || !jobIDPattern.MatchString(id) {
		return jobs.OutputSnapshot{}, errors.New("remote launcher is not configured")
	}
	raw, err := l.client.Call(ctx, struct {
		Operation string `json:"operation"`
		ID        string `json:"id"`
	}{Operation: "output", ID: id})
	if err != nil {
		return jobs.OutputSnapshot{}, err
	}
	var result jobs.OutputSnapshot
	if err = decodeStrict(raw, &result); err != nil || result.JobID != id || !result.State.Valid() ||
		len(result.Output) > jobs.MaxOutputBytes {
		return jobs.OutputSnapshot{}, errors.New("launcher returned an invalid output result")
	}
	return result, nil
}

func (l *Launcher) Cancel(ctx context.Context, id string) (jobs.CancelResult, error) {
	if l == nil || !jobIDPattern.MatchString(id) {
		return jobs.CancelResult{}, errors.New("remote launcher is not configured")
	}
	raw, err := l.client.Call(ctx, struct {
		Operation string `json:"operation"`
		ID        string `json:"id"`
	}{Operation: "cancel", ID: id})
	if err != nil {
		return jobs.CancelResult{}, err
	}
	var result jobs.CancelResult
	if err = decodeStrict(raw, &result); err != nil || result.JobID != id ||
		result.Status.JobID != id || !result.Status.State.Valid() {
		return jobs.CancelResult{}, errors.New("launcher returned an invalid cancel result")
	}
	return result, nil
}

func (l *Launcher) wait(ctx context.Context, id string) (jobs.Result, error) {
	raw, err := l.client.Call(ctx, struct {
		Operation string `json:"operation"`
		ID        string `json:"id"`
	}{Operation: "wait", ID: id})
	if err != nil {
		return jobs.Result{}, err
	}
	var result waitResult
	if err = decodeStrict(raw, &result); err != nil {
		return jobs.Result{}, errors.New("launcher returned an invalid wait result")
	}
	jobResult := jobs.Result{
		ExitCode: result.ExitCode, Outcome: result.Outcome,
		Output: jobs.Output{Text: result.Output, Truncated: result.Truncated}, Cleanup: result.Cleanup,
	}
	if !jobResult.Valid(jobs.MaxOutputBytes) {
		return jobs.Result{}, errors.New("launcher returned an invalid job result")
	}
	return jobResult, nil
}

func (l *Launcher) cleanup(id string) error {
	if l == nil || !jobIDPattern.MatchString(id) || l.cleanupTimeout <= 0 {
		return errors.New("remote launcher cleanup is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.cleanupTimeout)
	defer cancel()
	result, err := l.Cancel(ctx, id)
	if err != nil {
		return err
	}
	if result.Status.Cleanup == jobs.CleanupFailed {
		return errors.New("launcher reported incomplete cleanup")
	}
	return nil
}

func decodeStrict(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("launcher returned trailing result data")
	}
	return nil
}

func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	return errors.Join(primary, cleanup)
}
