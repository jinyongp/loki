package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"loki/internal/config"
	"loki/internal/fault"
	"loki/internal/policy"
)

func fixture(t *testing.T) *Controller {
	t.Helper()
	root := t.TempDir()
	paths, err := policy.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { paths.Close() })
	configuration, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	environment := []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test"}
	c := &Controller{Paths: paths, Config: configuration, Env: environment}
	c.Runner = testProcessRunner{Root: paths.Root(), Env: environment}
	if err := os.Mkdir(filepath.Join(root, "repo"), 0700); err != nil {
		t.Fatal(err)
	}
	git(t, c, "init", "-q", "--initial-branch=main")
	return c
}
func git(t *testing.T, c *Controller, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "/usr/bin/git", args...)
	cmd.Dir = filepath.Join(c.Paths.Root(), "repo")
	cmd.Env = c.Env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return string(output)
}
func write(t *testing.T, c *Controller, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.Paths.Root(), "repo", name), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func index(t *testing.T, c *Controller) string {
	t.Helper()
	result, err := c.Index(t.Context(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	return result["index_sha256"].(string)
}
func TestIndexPathsAndPartialPatch(t *testing.T) {
	c := fixture(t)
	write(t, c, "a.txt", "one\ntwo\n")
	write(t, c, "b.txt", "other\n")
	empty := index(t, c)
	if _, err := c.MutatePaths(t.Context(), "stage", "repo", []string{"a.txt"}, &empty); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MutatePaths(t.Context(), "stage", "repo", []string{"b.txt"}, &empty); err == nil {
		t.Fatal("stale index accepted")
	}
	if _, err := c.MutatePaths(t.Context(), "unstage", "repo", []string{"a.txt"}, nil); err != nil {
		t.Fatal(err)
	}
	if index(t, c) != empty {
		t.Fatal("unborn index not restored")
	}
	git(t, c, "add", "a.txt", "b.txt")
	git(t, c, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
	write(t, c, "a.txt", "changed\ntwo\n")
	write(t, c, "b.txt", "unrelated\n")
	if _, err := c.MutatePaths(t.Context(), "stage", "repo", []string{"b.txt"}, nil); err != nil {
		t.Fatal(err)
	}
	before := index(t, c)
	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n-one\n+changed\n two\n"
	result, err := c.StagePatch(t.Context(), "repo", patch, false, &before)
	if err != nil {
		t.Fatal(err)
	}
	if result["index_sha256"] == before {
		t.Fatal("patch not staged")
	}
	after := result["index_sha256"].(string)
	if _, err := c.StagePatch(t.Context(), "repo", patch, true, &after); err != nil {
		t.Fatal(err)
	}
	if index(t, c) != before {
		t.Fatal("reverse changed unrelated index entries")
	}
	if data, err := os.ReadFile(filepath.Join(c.Paths.Root(), "repo/a.txt")); err != nil || string(data) != "changed\ntwo\n" {
		t.Fatalf("worktree modified: %s %v", data, err)
	}
	if _, err := c.MutatePaths(t.Context(), "unstage", "repo", []string{"b.txt"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := git(t, c, "diff", "--cached"); got != "" {
		t.Fatal(got)
	}
	for _, bad := range []string{"../outside", ".git/config", "/etc/passwd"} {
		if _, err := c.MutatePaths(t.Context(), "stage", "repo", []string{bad}, nil); err == nil {
			t.Fatalf("accepted path %s", bad)
		}
	}
	if _, err := c.StagePatch(t.Context(), "repo", patch+"new file mode 120000\n", false, nil); err == nil {
		t.Fatal("mode patch accepted")
	}
	write(t, c, ":(glob)*", "literal\n")
	if _, err := c.MutatePaths(t.Context(), "stage", "repo", []string{":(glob)*"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestGitInspectionAndTemplates(t *testing.T) {
	c := fixture(t)
	write(t, c, "file.txt", "content\n")
	status, err := c.Status(t.Context(), "repo")
	if err != nil || !strings.Contains(status["output"].(string), "?? file.txt") {
		t.Fatalf("%v %v", status, err)
	}
	ctx, err := c.CommitContext(t.Context(), "repo")
	if err != nil || ctx["configured"] != false {
		t.Fatalf("%v %v", ctx, err)
	}
	write(t, c, "template.txt", "Subject\n\nBody\n")
	git(t, c, "config", "commit.template", "template.txt")
	ctx, err = c.CommitContext(t.Context(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	if ctx["template"].(map[string]any)["content"] != "Subject\n\nBody\n" {
		t.Fatal(ctx)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, c, "config", "commit.template", outside)
	if _, err := c.CommitContext(t.Context(), "repo"); err == nil {
		t.Fatal("outside template")
	}
	if err := os.Symlink(outside, filepath.Join(c.Paths.Root(), "repo/link")); err != nil {
		t.Fatal(err)
	}
	git(t, c, "config", "commit.template", "link")
	if _, err := c.CommitContext(t.Context(), "repo"); err == nil {
		t.Fatal("symbolic template")
	}
}

func TestGitConcurrentCASAndWorktree(t *testing.T) {
	c := fixture(t)
	write(t, c, "a.txt", "initial\n")
	git(t, c, "add", "a.txt")
	git(t, c, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "initial")
	git(t, c, "worktree", "add", "-b", "feature", filepath.Join(c.Paths.Root(), "feature"))
	if _, err := c.Index(t.Context(), "feature"); err != nil {
		t.Fatal(err)
	}
	write(t, c, "a.txt", "changed\n")
	write(t, c, "b.txt", "new\n")
	expected := index(t, c)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, path := range []string{"a.txt", "b.txt"} {
		wg.Go(func() {
			_, err := c.MutatePaths(t.Context(), "stage", "repo", []string{path}, &expected)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			success++
			continue
		}
		if detail := fault.Describe(err); detail.Code == fault.CodeConflict {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent staging error: %v", err)
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("CAS results: successes=%d conflicts=%d", success, conflicts)
	}
	external := filepath.Join(t.TempDir(), "metadata")
	cmd := exec.CommandContext(t.Context(), "/usr/bin/git", "init", "-q", "--separate-git-dir", external, filepath.Join(c.Paths.Root(), "foreign"))
	cmd.Env = c.Env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	if _, err := c.Index(t.Context(), "foreign"); err == nil {
		t.Fatal("external Git metadata accepted")
	}
}
