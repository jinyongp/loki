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

type startResult struct {
	ID string `json:"id"`
}

type waitResult struct {
	ExitCode int64 `json:"exit_code"`
}

type cancelResult struct {
	Canceled bool `json:"canceled"`
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
			Limits:      rpc.Limits{Timeout: options.Timeout},
		},
		policySHA256:   options.PolicySHA256,
		cleanupTimeout: cleanupTimeout,
	}, nil
}

func (l *Launcher) Run(ctx context.Context, workload jobs.Workload) (jobs.LaunchResult, error) {
	if l == nil || !digestPattern.MatchString(l.policySHA256) || !jobIDPattern.MatchString(workload.ID) {
		return jobs.LaunchResult{}, errors.New("remote launcher is not configured")
	}
	if err := l.start(ctx, workload); err != nil {
		return jobs.LaunchResult{}, joinCleanup(err, l.cleanup(workload.ID))
	}
	result, err := l.wait(ctx, workload.ID)
	if err != nil {
		return jobs.LaunchResult{}, joinCleanup(err, l.cleanup(workload.ID))
	}
	return result, nil
}

func (l *Launcher) start(ctx context.Context, workload jobs.Workload) error {
	request := struct {
		Operation    string   `json:"operation"`
		ID           string   `json:"id"`
		PolicySHA256 string   `json:"policy_sha256"`
		CWD          string   `json:"cwd"`
		Argv         []string `json:"argv"`
	}{
		Operation:    "start",
		ID:           workload.ID,
		PolicySHA256: l.policySHA256,
		CWD:          workload.CWD,
		Argv:         append([]string(nil), workload.Argv...),
	}
	raw, err := l.client.Call(ctx, request)
	if err != nil {
		return err
	}
	var result startResult
	if err = decodeStrict(raw, &result); err != nil || result.ID != workload.ID {
		return errors.New("launcher returned an invalid start result")
	}
	return nil
}

func (l *Launcher) wait(ctx context.Context, id string) (jobs.LaunchResult, error) {
	raw, err := l.client.Call(ctx, struct {
		Operation string `json:"operation"`
		ID        string `json:"id"`
	}{Operation: "wait", ID: id})
	if err != nil {
		return jobs.LaunchResult{}, err
	}
	var result waitResult
	if err = decodeStrict(raw, &result); err != nil {
		return jobs.LaunchResult{}, errors.New("launcher returned an invalid wait result")
	}
	if result.ExitCode < 0 || result.ExitCode > 255 {
		return jobs.LaunchResult{}, errors.New("launcher returned an invalid exit code")
	}
	return jobs.LaunchResult{ExitCode: result.ExitCode}, nil
}

func (l *Launcher) cleanup(id string) error {
	if l == nil || !jobIDPattern.MatchString(id) || l.cleanupTimeout <= 0 {
		return errors.New("remote launcher cleanup is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.cleanupTimeout)
	defer cancel()
	raw, err := l.client.Call(ctx, struct {
		Operation string `json:"operation"`
		ID        string `json:"id"`
	}{Operation: "cancel", ID: id})
	if err != nil {
		return err
	}
	var result cancelResult
	if err = decodeStrict(raw, &result); err != nil {
		return errors.New("launcher returned an invalid cancel result")
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
