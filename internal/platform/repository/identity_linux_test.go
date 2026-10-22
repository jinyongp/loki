//go:build linux

package repository

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIdentityTracksNearestRepositoryAndLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "/usr/bin/git", args...)
		cmd.Dir = root
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1"}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Git fixture failed: %v: %s", err, output)
		}
	}
	git("init", "-q", "main")
	git("-C", "main", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial")
	git("-C", "main", "worktree", "add", "-q", "-b", "linked", filepath.Join(root, "linked"))
	git("init", "-q", "main/nested")
	resolver, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	main, err := resolver.Identity("main")
	if err != nil {
		t.Fatal(err)
	}
	linked, err := resolver.Identity("linked")
	if err != nil || linked != main {
		t.Fatal("linked worktree lost owning repository")
	}
	nested, err := resolver.Identity("main/nested")
	if err != nil || nested == main {
		t.Fatal("nested repository inherited outer grant")
	}
	if os.Symlink("main", filepath.Join(root, "alias")) != nil {
		t.Fatal("symlink fixture")
	}
	for _, cwd := range []string{".", "../main", "alias", "main/.git/../../alias"} {
		if _, err := resolver.Identity(cwd); err == nil {
			t.Fatal("invalid repository path authorized", cwd)
		}
	}
	git("init", "-q", "--separate-git-dir", filepath.Join(t.TempDir(), "foreign"), "foreign")
	if _, err := resolver.Identity("foreign"); err == nil {
		t.Fatal("external metadata accepted")
	}
}
