package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGitAt(t *testing.T, c *Controller, directory string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "/usr/bin/git", args...)
	command.Dir = directory
	command.Env = c.Env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v at %s: %v: %s", args, directory, err, output)
	}
	return string(output)
}

func TestTrackedFileUsesNearestRepositoryAndWorkspaceMetadata(t *testing.T) {
	c := fixture(t)
	outer := filepath.Join(c.Paths.Root(), "repo")
	write(t, c, "a.txt", "outer\n")
	git(t, c, "add", "--", "a.txt")
	git(t, c, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "outer")

	nested := filepath.Join(outer, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	runGitAt(t, c, nested, "init", "-q")
	if err := os.WriteFile(filepath.Join(nested, "tracked.txt"), []byte("nested\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "untracked.txt"), []byte("untracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitAt(t, c, nested, "add", "--", "tracked.txt")

	for path, want := range map[string]bool{
		"repo/nested/tracked.txt":   true,
		"repo/nested/untracked.txt": false,
	} {
		got, err := c.TrackedFile(t.Context(), path)
		if err != nil || got != want {
			t.Fatalf("TrackedFile(%s) = %v, %v; want %v", path, got, err, want)
		}
	}

	// An in-workspace linked worktree uses metadata under the original repository
	// and remains a valid owning repository.
	feature := filepath.Join(c.Paths.Root(), "feature")
	git(t, c, "worktree", "add", "-b", "feature", feature)
	tracked, err := c.TrackedFile(t.Context(), "feature/a.txt")
	if err != nil || !tracked {
		t.Fatalf("worktree tracked file = %v, %v", tracked, err)
	}

	// A worktree whose metadata is outside the approved workspace must fail
	// closed rather than being treated as an ordinary untracked file.
	external := filepath.Join(t.TempDir(), "metadata")
	foreign := filepath.Join(c.Paths.Root(), "foreign")
	command := exec.CommandContext(t.Context(), "/usr/bin/git", "init", "-q", "--separate-git-dir", external, foreign)
	command.Env = c.Env
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("foreign init: %v: %s", runErr, output)
	}
	if err := os.WriteFile(filepath.Join(foreign, "file.txt"), []byte("foreign\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitAt(t, c, foreign, "add", "--", "file.txt")
	if _, err := c.TrackedFile(t.Context(), "foreign/file.txt"); err == nil || !strings.Contains(err.Error(), "metadata escapes workspace") {
		t.Fatalf("external metadata error = %v", err)
	}
}
