package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitStagesDeletedAndRenamedPaths(t *testing.T) {
	c := fixture(t)
	if err := os.Mkdir(filepath.Join(c.Paths.Root(), "repo", "gone"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, c, "move-old.txt", "move\n")
	write(t, c, "deleted.txt", "deleted\n")
	write(t, c, "gone/child.txt", "child\n")
	git(t, c, "add", "--", "move-old.txt", "deleted.txt", "gone/child.txt")
	git(t, c, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")

	repo := filepath.Join(c.Paths.Root(), "repo")
	if err := os.Rename(filepath.Join(repo, "move-old.txt"), filepath.Join(repo, "move-new.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(repo, "gone")); err != nil {
		t.Fatal(err)
	}

	for _, requested := range []string{"move-old.txt", "deleted.txt", "gone"} {
		diff, err := c.Diff(t.Context(), "repo", false, &requested)
		if err != nil || diff["output"].(string) == "" {
			t.Fatalf("deleted diff %s = %#v, %v", requested, diff, err)
		}
	}
	unknown := "missing-never-tracked.txt"
	if _, err := c.Diff(t.Context(), "repo", false, &unknown); err == nil || !strings.Contains(err.Error(), "does not exist and is not tracked") {
		t.Fatalf("unknown diff error = %v", err)
	}

	before := index(t, c)
	if _, err := c.MutatePaths(t.Context(), "stage", "repo", []string{"move-old.txt", "move-new.txt", "deleted.txt", "gone"}, &before); err != nil {
		t.Fatal(err)
	}
	cached := "deleted.txt"
	diff, err := c.Diff(t.Context(), "repo", true, &cached)
	if err != nil || !strings.Contains(diff["output"].(string), "deleted") {
		t.Fatalf("cached deleted diff = %#v, %v", diff, err)
	}
	after := index(t, c)
	if _, err = c.MutatePaths(t.Context(), "stage", "repo", []string{unknown}, &after); err == nil || !strings.Contains(err.Error(), "does not exist and is not tracked") {
		t.Fatalf("unknown stage error = %v", err)
	}
	if got := index(t, c); got != after {
		t.Fatal("rejected absent stage changed index")
	}

	nameStatus := git(t, c, "diff", "--cached", "--name-status")
	for _, path := range []string{"move-old.txt", "move-new.txt", "deleted.txt", "gone/child.txt"} {
		if !strings.Contains(nameStatus, path) {
			t.Fatalf("cached change missing %s: %s", path, nameStatus)
		}
	}
}

func TestGitDeletedPathValidationUsesHeadAfterStagedDeletion(t *testing.T) {
	c := fixture(t)
	write(t, c, "deleted.txt", "before\n")
	git(t, c, "add", "--", "deleted.txt")
	git(t, c, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
	if err := os.Remove(filepath.Join(c.Paths.Root(), "repo", "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	before := index(t, c)
	if _, err := c.MutatePaths(t.Context(), "stage", "repo", []string{"deleted.txt"}, &before); err != nil {
		t.Fatal(err)
	}
	requested := "deleted.txt"
	result, err := c.Diff(t.Context(), "repo", true, &requested)
	if err != nil || result["output"].(string) == "" {
		t.Fatalf("HEAD-known staged deletion = %#v, %v", result, err)
	}
}
