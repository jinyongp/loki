package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "/usr/bin/git", args...)
	command.Dir = directory
	command.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0",
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestRemoveTrackedUsesNearestNestedRepository(t *testing.T) {
	files := fixture(t)
	root := files.Policy.Root()
	repository := filepath.Join(root, "projects", "nested")
	if err := os.MkdirAll(repository, 0700); err != nil {
		t.Fatal(err)
	}

	// The outer repository may already know a path that later becomes part of a
	// nested repository. Removal must still use the nearest repository rather
	// than treating the outer index as authority for the nested file.
	gitIn(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(repository, "untracked.txt"), []byte("untracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "--", "projects/nested/untracked.txt")

	gitIn(t, repository, "init", "-q")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("tracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repository, "add", "--", "tracked.txt")

	untracked := "projects/nested/untracked.txt"
	if _, err := files.RemoveTracked(t.Context(), untracked); err == nil || !strings.Contains(err.Error(), "only Git-tracked files") {
		t.Fatalf("untracked removal error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(files.Policy.Root(), untracked)); err != nil {
		t.Fatalf("untracked file changed: %v", err)
	}

	tracked := "projects/nested/tracked.txt"
	removed, err := files.RemoveTracked(t.Context(), tracked)
	if err != nil {
		t.Fatal(err)
	}
	revision, _ := removed["previous_revision"].(string)
	if revision == "" {
		t.Fatal("tracked removal did not capture recovery revision")
	}
	if _, err = os.Stat(filepath.Join(files.Policy.Root(), tracked)); !os.IsNotExist(err) {
		t.Fatalf("tracked file was not removed: %v", err)
	}
	if _, err = files.Restore(tracked, revision, "missing"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(files.Policy.Root(), tracked)); err != nil || string(data) != "tracked\n" {
		t.Fatalf("restored data = %q, %v", data, err)
	}
}
