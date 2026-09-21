package gitops

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"loki/internal/work/jobs"
)

type CommandRequest struct {
	CWD       string
	Argv      []string
	Input     []byte
	Timeout   time.Duration
	MaxOutput int
}

type CommandResult struct {
	ExitCode  int
	Output    string
	Raw       []byte
	Truncated bool
	TimedOut  bool
	Canceled  bool
}

type Runner interface {
	Run(context.Context, CommandRequest) (CommandResult, error)
}

type JobRunner struct {
	Jobs jobs.Runner
}

func (r JobRunner) Run(ctx context.Context, request CommandRequest) (CommandResult, error) {
	if r.Jobs == nil {
		return CommandResult{}, errors.New("Git Job runner is not configured")
	}
	if request.Timeout <= 0 {
		return CommandResult{}, errors.New("Git command timeout is invalid")
	}
	if request.MaxOutput < 1 || request.MaxOutput > jobs.MaxRunOutputBytes {
		return CommandResult{}, errors.New("Git command output limit is invalid")
	}
	runCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	result, err := r.Jobs.Run(runCtx, jobs.RunRequest{
		CWD: request.CWD, Argv: append([]string(nil), request.Argv...),
		Input: append([]byte(nil), request.Input...), MaxOutputBytes: request.MaxOutput,
	})
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return CommandResult{ExitCode: 124, TimedOut: true}, nil
		}
		if errors.Is(runCtx.Err(), context.Canceled) {
			return CommandResult{ExitCode: 130, Canceled: true}, nil
		}
		return CommandResult{}, err
	}
	if result.Cleanup != jobs.CleanupComplete && result.Cleanup != jobs.CleanupNotRequired {
		return CommandResult{}, errors.New("Git Job cleanup did not complete")
	}
	if result.Outcome == jobs.OutcomeLaunchFailed || result.Outcome == jobs.OutcomeUnknown {
		return CommandResult{}, errors.New("Git Job did not produce a reliable command result")
	}
	raw := append([]byte(nil), result.Output...)
	output, truncated := boundedCommandText(raw, request.MaxOutput, result.Truncated)
	exitCode := 0
	if result.ExitCode != nil {
		exitCode = int(*result.ExitCode)
	}
	switch result.Outcome {
	case jobs.OutcomeTimedOut:
		exitCode = 124
		return CommandResult{ExitCode: exitCode, Output: output, Raw: raw, Truncated: truncated, TimedOut: true}, nil
	case jobs.OutcomeCanceled:
		exitCode = 130
		return CommandResult{ExitCode: exitCode, Output: output, Raw: raw, Truncated: truncated, Canceled: true}, nil
	default:
		return CommandResult{ExitCode: exitCode, Output: output, Raw: raw, Truncated: truncated}, nil
	}
}

func boundedCommandText(raw []byte, limit int, truncated bool) (string, bool) {
	var text strings.Builder
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		if r == utf8.RuneError && size == 1 {
			width := 1
			switch {
			case raw[0] >= 0xc2 && raw[0] <= 0xdf:
				width = 2
			case raw[0] >= 0xe0 && raw[0] <= 0xef:
				width = 3
			case raw[0] >= 0xf0 && raw[0] <= 0xf4:
				width = 4
			}
			for size < width && size < len(raw) {
				b := raw[size]
				if b < 0x80 || b > 0xbf || size == 1 && (raw[0] == 0xe0 && b < 0xa0 || raw[0] == 0xed && b > 0x9f || raw[0] == 0xf0 && b < 0x90 || raw[0] == 0xf4 && b > 0x8f) {
					break
				}
				size++
			}
			if truncated && size < width && size == len(raw) {
				break
			}
		}
		encoded := string(r)
		if text.Len()+len(encoded) > limit {
			truncated = true
			break
		}
		text.WriteString(encoded)
		raw = raw[size:]
	}
	return text.String(), truncated
}
