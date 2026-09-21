package gitops

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/process"
	"loki/internal/work/jobs"
)

type testProcessRunner struct {
	Root string
	Env  []string
}

func (r testProcessRunner) Run(ctx context.Context, request CommandRequest) (CommandResult, error) {
	cwd := r.Root
	if request.CWD != "." {
		cwd = filepath.Join(r.Root, filepath.FromSlash(request.CWD))
	}
	result, err := process.Run(ctx, process.Spec{
		Argv: request.Argv, CWD: cwd, Env: r.Env, Input: request.Input,
		Timeout: request.Timeout, MaxOutput: request.MaxOutput,
	})
	root := filepath.Clean(r.Root)
	output := strings.ReplaceAll(result.Output, root, "/workspace")
	raw := bytes.ReplaceAll(result.Raw, []byte(root), []byte("/workspace"))
	return CommandResult{
		ExitCode: result.ExitCode, Output: output, Raw: raw,
		Truncated: result.Truncated, TimedOut: result.TimedOut, Canceled: result.Canceled,
	}, err
}

type fakeJobRunner struct {
	request jobs.RunRequest
	result  jobs.RunResult
	err     error
}

func (r *fakeJobRunner) Run(_ context.Context, request jobs.RunRequest) (jobs.RunResult, error) {
	request.Argv = append([]string(nil), request.Argv...)
	request.Input = append([]byte(nil), request.Input...)
	r.request = request
	return r.result, r.err
}

func TestJobRunnerPreservesRawBytesAndBounds(t *testing.T) {
	exitCode := int64(0)
	fake := &fakeJobRunner{result: jobs.RunResult{
		JobID: strings.Repeat("a", 32), ExitCode: &exitCode, Outcome: jobs.OutcomeExited,
		Output: []byte{'a', 0, 'b'}, Cleanup: jobs.CleanupComplete,
	}}
	runner := JobRunner{Jobs: fake}
	result, err := runner.Run(t.Context(), CommandRequest{
		CWD: "repo", Argv: []string{"/usr/bin/git", "status"}, Input: []byte("stdin"),
		Timeout: time.Second, MaxOutput: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || string(result.Raw) != string([]byte{'a', 0, 'b'}) ||
		result.Output != string([]byte{'a', 0, 'b'}) || result.Truncated {
		t.Fatalf("result = %#v", result)
	}
	if fake.request.CWD != "repo" || fake.request.MaxOutputBytes != 4096 ||
		string(fake.request.Input) != "stdin" || len(fake.request.Argv) != 2 {
		t.Fatalf("request = %#v", fake.request)
	}
}
