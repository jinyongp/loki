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

const maxRunTimeout = 24 * time.Hour

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
	client       rpc.Client
	policySHA256 string
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
	expectedUID := *options.ExpectedUID
	return &Launcher{
		client: rpc.Client{
			Socket:      options.Socket,
			ExpectedUID: &expectedUID,
			Limits:      rpc.Limits{Timeout: options.Timeout},
		},
		policySHA256: options.PolicySHA256,
	}, nil
}

func (l *Launcher) Run(ctx context.Context, workload jobs.Workload) (jobs.LaunchResult, error) {
	if l == nil || !digestPattern.MatchString(l.policySHA256) || !jobIDPattern.MatchString(workload.ID) {
		return jobs.LaunchResult{}, errors.New("remote launcher is not configured")
	}
	request := struct {
		Operation    string   `json:"operation"`
		ID           string   `json:"id"`
		PolicySHA256 string   `json:"policy_sha256"`
		CWD          string   `json:"cwd"`
		Argv         []string `json:"argv"`
	}{
		Operation:    "run",
		ID:           workload.ID,
		PolicySHA256: l.policySHA256,
		CWD:          workload.CWD,
		Argv:         append([]string(nil), workload.Argv...),
	}
	raw, err := l.client.Call(ctx, request)
	if err != nil {
		return jobs.LaunchResult{}, err
	}
	var result struct {
		ExitCode int64 `json:"exit_code"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return jobs.LaunchResult{}, errors.New("launcher returned an invalid result")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return jobs.LaunchResult{}, errors.New("launcher returned trailing result data")
	}
	if result.ExitCode < 0 || result.ExitCode > 255 {
		return jobs.LaunchResult{}, errors.New("launcher returned an invalid exit code")
	}
	return jobs.LaunchResult{ExitCode: result.ExitCode}, nil
}
