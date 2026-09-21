package service

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/config"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/policy"
	"loki/internal/process"
	"loki/internal/work/jobs"
)

type serviceTestJobRunner struct {
	root string
	env  []string
}

func (r serviceTestJobRunner) Run(ctx context.Context, request jobs.RunRequest) (jobs.RunResult, error) {
	cwd := r.root
	if request.CWD != "." {
		cwd = filepath.Join(r.root, filepath.FromSlash(request.CWD))
	}
	maximum := request.MaxOutputBytes
	if maximum == 0 {
		maximum = jobs.MaxOutputBytes
	}
	result, err := process.Run(ctx, process.Spec{
		Argv: request.Argv, CWD: cwd, Env: r.env, Input: request.Input,
		Timeout: 30 * time.Second, MaxOutput: maximum,
	})
	if err != nil {
		return jobs.RunResult{}, err
	}
	outcome := jobs.OutcomeExited
	if result.TimedOut {
		outcome = jobs.OutcomeTimedOut
	}
	if result.Canceled {
		outcome = jobs.OutcomeCanceled
	}
	exitCode := int64(result.ExitCode)
	root := filepath.Clean(r.root)
	raw := bytes.ReplaceAll(result.Raw, []byte(root), []byte("/workspace"))
	if len(raw) == 0 && result.Output != "" {
		raw = []byte(strings.ReplaceAll(result.Output, root, "/workspace"))
	}
	return jobs.RunResult{
		JobID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ExitCode: &exitCode, Outcome: outcome,
		Output: raw, Truncated: result.Truncated, Cleanup: jobs.CleanupComplete,
	}, nil
}

func gitJobsFixture(t *testing.T, c config.Config, environment map[string]string) jobs.Runner {
	t.Helper()
	values := map[string]string{}
	for key, value := range environment {
		values[key] = value
	}
	if values["HOME"] == "" {
		values["HOME"] = t.TempDir()
	}
	if values["GIT_CONFIG_GLOBAL"] == "" {
		values["GIT_CONFIG_GLOBAL"] = "/dev/null"
	}
	if values["GIT_CONFIG_NOSYSTEM"] == "" {
		values["GIT_CONFIG_NOSYSTEM"] = "1"
	}
	return serviceTestJobRunner{root: c.Root, env: toolEnvironment(values)}
}

type emptyJobToolchainResolver struct{}

func (emptyJobToolchainResolver) Resolve(context.Context, string) ([]jobs.ToolchainRef, error) {
	return nil, nil
}

func policyGenerationFixture(t *testing.T) controlpolicy.Generation {
	t.Helper()
	generation, err := controlpolicy.NewGeneration(map[string]any{"fixture": true})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func serviceFixture(t *testing.T) *policy.Workspace {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "initial"}, {"-C", repo, "worktree", "add", "-q", "-b", "feature", filepath.Join(workspace, "feature")}} {
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Env = []string{"HOME=" + root, "PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s %v", out, err)
		}
	}
	paths, err := policy.New(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { paths.Close() })
	return paths
}
