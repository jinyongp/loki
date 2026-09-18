package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/policy"
)

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
