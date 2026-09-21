package service

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/config"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/gitops"
	"loki/internal/policy"
	"loki/internal/process"
)

type serviceTestGitRunner struct {
	root string
	env  []string
}

func (r serviceTestGitRunner) Run(ctx context.Context, request gitops.CommandRequest) (gitops.CommandResult, error) {
	cwd := r.root
	if request.CWD != "." {
		cwd = filepath.Join(r.root, filepath.FromSlash(request.CWD))
	}
	result, err := process.Run(ctx, process.Spec{
		Argv: request.Argv, CWD: cwd, Env: r.env, Input: request.Input,
		Timeout: request.Timeout, MaxOutput: request.MaxOutput,
	})
	root := filepath.Clean(r.root)
	output := strings.ReplaceAll(result.Output, root, "/workspace")
	raw := bytes.ReplaceAll(result.Raw, []byte(root), []byte("/workspace"))
	return gitops.CommandResult{
		ExitCode: result.ExitCode, Output: output, Raw: raw,
		Truncated: result.Truncated, TimedOut: result.TimedOut, Canceled: result.Canceled,
	}, err
}

func gitRunnerFixture(t *testing.T, c config.Config, environment map[string]string) gitops.Runner {
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
	return serviceTestGitRunner{root: c.Root, env: toolEnvironment(values)}
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
