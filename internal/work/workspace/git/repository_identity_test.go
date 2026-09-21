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

func TestContextEvidenceSharesRepositoryIdentityAcrossWorktreesAndTracksCode(t *testing.T) {
	c := fixture(t)
	write(t, c, "tracked.txt", "base\n")
	git(t, c, "add", "--", "tracked.txt")
	git(t, c, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "base")

	mainEvidence, err := c.ContextEvidence(t.Context(), "repo", ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(mainEvidence.RepositoryID) != 64 || len(mainEvidence.WorktreeID) != 64 || len(mainEvidence.CodeBasis) != 64 || len(mainEvidence.Gaps) != 0 {
		t.Fatalf("main evidence = %#v", mainEvidence)
	}

	feature := filepath.Join(c.Paths.Root(), "feature-context")
	runGitAt(t, c, filepath.Join(c.Paths.Root(), "repo"), "worktree", "add", "-q", "-b", "feature-context", feature)
	featureEvidence, err := c.ContextEvidence(t.Context(), ".", "feature-context/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if featureEvidence.RepositoryID != mainEvidence.RepositoryID || featureEvidence.WorktreeID == mainEvidence.WorktreeID {
		t.Fatalf("worktree identities main=%#v feature=%#v", mainEvidence, featureEvidence)
	}

	if err := os.WriteFile(filepath.Join(c.Paths.Root(), "repo", "tracked.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := c.ContextEvidence(t.Context(), "repo", ".")
	if err != nil {
		t.Fatal(err)
	}
	if changed.CodeBasis == mainEvidence.CodeBasis || len(changed.Gaps) != 0 {
		t.Fatalf("tracked change evidence = %#v", changed)
	}

	if err := os.WriteFile(filepath.Join(c.Paths.Root(), "repo", "untracked.txt"), []byte("private content is not hashed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	untracked, err := c.ContextEvidence(t.Context(), "repo", ".")
	if err != nil {
		t.Fatal(err)
	}
	foundGap := false
	for _, gap := range untracked.Gaps {
		if gap == "untracked_content_unobserved" {
			foundGap = true
		}
	}
	if len(untracked.CodeBasis) != 64 || !foundGap {
		t.Fatalf("untracked evidence = %#v", untracked)
	}
	if untracked.RepositoryID != mainEvidence.RepositoryID || untracked.WorktreeID != mainEvidence.WorktreeID {
		t.Fatalf("identity changed with worktree contents: %#v", untracked)
	}
}
