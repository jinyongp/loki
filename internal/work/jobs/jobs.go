// Package jobs owns constrained one-shot workload execution use cases.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path"
	"regexp"
	"strings"
)

const (
	maxArgs            = 256
	maxArgBytes        = 64 << 10
	MaxRunInputBytes   = 64 << 20
	MaxRunOutputBytes  = 64 << 20
	MaxRunRequestBytes = 96 << 20
	MaxRunResultBytes  = 96 << 20
)

var jobIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type RunRequest struct {
	CWD            string   `json:"cwd"`
	Argv           []string `json:"argv"`
	Input          []byte   `json:"input,omitempty"`
	MaxOutputBytes int      `json:"max_output_bytes,omitempty"`
}

type Workload struct {
	ID             string
	RequestID      string
	RequestSHA256  string
	CWD            string
	Argv           []string
	TimeoutSeconds int
	Network        NetworkProfile
	Endpoints      []EndpointRequest
	Input          []byte
	InputPath      string
	MaxOutputBytes int
}

type RunExecutionResult struct {
	ExitCode  *int64        `json:"exit_code,omitempty"`
	Outcome   Outcome       `json:"outcome"`
	Output    []byte        `json:"output"`
	Truncated bool          `json:"truncated"`
	Cleanup   CleanupStatus `json:"cleanup"`
}

func (r RunExecutionResult) Valid(maxOutputBytes int) bool {
	if maxOutputBytes < 1 || maxOutputBytes > MaxRunOutputBytes ||
		!r.Outcome.Valid() || !r.Cleanup.Valid() || len(r.Output) > maxOutputBytes {
		return false
	}
	if r.ExitCode != nil && (*r.ExitCode < 0 || *r.ExitCode > 255) {
		return false
	}
	if (r.Outcome == OutcomeExited || r.Outcome == OutcomeOOMKilled) && r.ExitCode == nil {
		return false
	}
	if r.Outcome == OutcomeLaunchFailed && r.ExitCode != nil {
		return false
	}
	return true
}

type RunResult struct {
	JobID     string        `json:"job_id"`
	ExitCode  *int64        `json:"exit_code,omitempty"`
	Outcome   Outcome       `json:"outcome"`
	Output    []byte        `json:"output"`
	Truncated bool          `json:"truncated"`
	Cleanup   CleanupStatus `json:"cleanup"`
}

func (r RunResult) Valid(maxOutputBytes int) bool {
	return jobIDPattern.MatchString(r.JobID) && RunExecutionResult{
		ExitCode: r.ExitCode, Outcome: r.Outcome, Output: r.Output,
		Truncated: r.Truncated, Cleanup: r.Cleanup,
	}.Valid(maxOutputBytes)
}

type IDSource func() (string, error)

type Service struct {
	launcher Launcher
	newID    IDSource
}

func NewService(launcher Launcher, newID IDSource) (*Service, error) {
	if launcher == nil {
		return nil, errors.New("jobs requires a launcher")
	}
	if newID == nil {
		return nil, errors.New("jobs requires an ID source")
	}
	return &Service{launcher: launcher, newID: newID}, nil
}

func RandomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Service) Start(ctx context.Context, request StartRequest) (StartResult, error) {
	if s == nil || s.launcher == nil {
		return StartResult{}, errors.New("jobs service is not configured")
	}
	normalized, fingerprint, err := normalizeStartRequest(request)
	if err != nil {
		return StartResult{}, err
	}
	id, err := JobIDForRequestID(normalized.RequestID)
	if err != nil {
		return StartResult{}, err
	}
	result, err := s.launcher.Start(ctx, Workload{
		ID: id, RequestID: normalized.RequestID, RequestSHA256: fingerprint, CWD: normalized.CWD,
		Argv: append([]string(nil), normalized.Argv...), TimeoutSeconds: normalized.TimeoutSeconds,
		Network: normalized.Network, Endpoints: append([]EndpointRequest(nil), normalized.Endpoints...),
	})
	if err != nil {
		return StartResult{}, err
	}
	if !validStartResult(result, normalized.RequestID, id) {
		return StartResult{}, errors.New("launcher returned an invalid start result")
	}
	return result, nil
}

func (s *Service) Inspect(ctx context.Context, id string) (Status, error) {
	if s == nil || s.launcher == nil {
		return Status{}, errors.New("jobs service is not configured")
	}
	if err := ValidateJobID(id); err != nil {
		return Status{}, err
	}
	return s.launcher.Inspect(ctx, id)
}

func (s *Service) Output(ctx context.Context, id string) (OutputSnapshot, error) {
	if s == nil || s.launcher == nil {
		return OutputSnapshot{}, errors.New("jobs service is not configured")
	}
	if err := ValidateJobID(id); err != nil {
		return OutputSnapshot{}, err
	}
	return s.launcher.Output(ctx, id)
}

func (s *Service) Cancel(ctx context.Context, id string) (CancelResult, error) {
	if s == nil || s.launcher == nil {
		return CancelResult{}, errors.New("jobs service is not configured")
	}
	if err := ValidateJobID(id); err != nil {
		return CancelResult{}, err
	}
	return s.launcher.Cancel(ctx, id)
}

func (s *Service) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if s == nil || s.launcher == nil || s.newID == nil {
		return RunResult{}, errors.New("jobs service is not configured")
	}
	cwd, err := validateCWD(request.CWD)
	if err != nil {
		return RunResult{}, err
	}
	argv, err := validateArgv(request.Argv)
	if err != nil {
		return RunResult{}, err
	}
	if len(request.Input) > MaxRunInputBytes {
		return RunResult{}, errors.New("job input exceeds its size limit")
	}
	maximum := request.MaxOutputBytes
	if maximum == 0 {
		maximum = MaxOutputBytes
	}
	if maximum < 1 || maximum > MaxRunOutputBytes {
		return RunResult{}, errors.New("job output limit is outside the supported range")
	}
	id, err := s.newID()
	if err != nil {
		return RunResult{}, err
	}
	if !jobIDPattern.MatchString(id) {
		return RunResult{}, errors.New("jobs ID source returned an invalid ID")
	}
	result, err := s.launcher.Run(ctx, Workload{
		ID: id, CWD: cwd, Argv: argv,
		Input: append([]byte(nil), request.Input...), MaxOutputBytes: maximum,
	})
	if err != nil {
		return RunResult{}, err
	}
	if !result.Valid(maximum) {
		return RunResult{}, errors.New("launcher returned an invalid job result")
	}
	return RunResult{
		JobID: id, ExitCode: result.ExitCode, Outcome: result.Outcome,
		Output: append([]byte(nil), result.Output...), Truncated: result.Truncated, Cleanup: result.Cleanup,
	}, nil
}

func validateCWD(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) || len(value) > 4096 || path.IsAbs(value) {
		return "", errors.New("job working directory is invalid")
	}
	clean := path.Clean(value)
	if clean != value || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("job working directory is invalid")
	}
	return clean, nil
}

func validateArgv(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > maxArgs || !path.IsAbs(values[0]) || path.Clean(values[0]) != values[0] || values[0] == "/" {
		return nil, errors.New("job command is invalid")
	}
	result := make([]string, len(values))
	total := 0
	for index, value := range values {
		if strings.ContainsRune(value, 0) || len(value) > 4096 {
			return nil, errors.New("job command is invalid")
		}
		total += len(value)
		if total > maxArgBytes {
			return nil, errors.New("job command exceeds its size limit")
		}
		result[index] = value
	}
	return result, nil
}
