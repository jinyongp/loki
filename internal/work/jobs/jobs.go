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
	maxArgs     = 256
	maxArgBytes = 64 << 10
)

var jobIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type RunRequest struct {
	CWD  string   `json:"cwd"`
	Argv []string `json:"argv"`
}

type Workload struct {
	ID   string
	CWD  string
	Argv []string
}

type LaunchResult struct {
	ExitCode int64
}

type RunResult struct {
	JobID    string `json:"job_id"`
	ExitCode int64  `json:"exit_code"`
}

type Launcher interface {
	Run(context.Context, Workload) (LaunchResult, error)
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
	id, err := s.newID()
	if err != nil {
		return RunResult{}, err
	}
	if !jobIDPattern.MatchString(id) {
		return RunResult{}, errors.New("jobs ID source returned an invalid ID")
	}
	result, err := s.launcher.Run(ctx, Workload{ID: id, CWD: cwd, Argv: argv})
	if err != nil {
		return RunResult{}, err
	}
	if result.ExitCode < 0 || result.ExitCode > 255 {
		return RunResult{}, errors.New("launcher returned an invalid exit code")
	}
	return RunResult{JobID: id, ExitCode: result.ExitCode}, nil
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
