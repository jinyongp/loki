package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func assertMarkerAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("repository executable ran: %s: %v", path, err)
		}
	}
}

func TestGitInspectionDisablesRepositoryExecutables(t *testing.T) {
	c := fixture(t)
	write(t, c, "file.txt", "before\n")
	write(t, c, ".gitattributes", "*.txt diff=probe\n")
	git(t, c, "add", "--", "file.txt", ".gitattributes")
	git(t, c, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")

	root := t.TempDir()
	fsmonitorMarker := filepath.Join(root, "fsmonitor-ran")
	fsmonitor := filepath.Join(root, "fsmonitor.sh")
	writeExecutable(t, fsmonitor, "#!/bin/sh\nprintf ran > '"+fsmonitorMarker+"'\nprintf '0\\n'\n")
	textconvMarker := filepath.Join(root, "textconv-ran")
	textconv := filepath.Join(root, "textconv.sh")
	writeExecutable(t, textconv, "#!/bin/sh\nprintf ran > '"+textconvMarker+"'\ncat \"$1\"\n")
	externalMarker := filepath.Join(root, "external-diff-ran")
	external := filepath.Join(root, "external-diff.sh")
	writeExecutable(t, external, "#!/bin/sh\nprintf ran > '"+externalMarker+"'\nexit 0\n")

	git(t, c, "config", "core.fsmonitor", fsmonitor)
	git(t, c, "config", "diff.probe.textconv", textconv)
	git(t, c, "config", "diff.external", external)
	write(t, c, "file.txt", "after\n")

	status, err := c.Status(t.Context(), "repo")
	if err != nil || !strings.Contains(status["output"].(string), "file.txt") {
		t.Fatalf("status = %#v, %v", status, err)
	}
	assertMarkerAbsent(t, fsmonitorMarker, textconvMarker, externalMarker)

	diff, err := c.Diff(t.Context(), "repo", false, nil)
	if err != nil || !strings.Contains(diff["output"].(string), "after") {
		t.Fatalf("diff = %#v, %v", diff, err)
	}
	assertMarkerAbsent(t, fsmonitorMarker, textconvMarker, externalMarker)
}

func TestGitRejectsExecutableFiltersBeforeWorktreeOperations(t *testing.T) {
	for _, key := range []string{"filter.probe.clean", "filter.probe.smudge", "filter.probe.process"} {
		t.Run(key, func(t *testing.T) {
			c := fixture(t)
			write(t, c, "file.txt", "before\n")
			write(t, c, ".gitattributes", "*.txt filter=probe\n")
			git(t, c, "add", "--", "file.txt", ".gitattributes")
			git(t, c, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")

			marker := filepath.Join(t.TempDir(), "filter-ran")
			filter := filepath.Join(t.TempDir(), "filter.sh")
			writeExecutable(t, filter, "#!/bin/sh\nprintf ran > '"+marker+"'\ncat\n")
			git(t, c, "config", key, filter)
			write(t, c, "file.txt", "after\n")
			before := index(t, c)

			for name, call := range map[string]func() error{
				"status": func() error { _, err := c.Status(t.Context(), "repo"); return err },
				"diff":   func() error { _, err := c.Diff(t.Context(), "repo", false, nil); return err },
				"stage": func() error {
					_, err := c.MutatePaths(t.Context(), "stage", "repo", []string{"file.txt"}, &before)
					return err
				},
			} {
				err := call()
				if err == nil || !strings.Contains(err.Error(), "executable Git filters") {
					t.Fatalf("%s with %s error = %v", name, key, err)
				}
				assertMarkerAbsent(t, marker)
				if got := index(t, c); got != before {
					t.Fatalf("%s with %s changed index", name, key)
				}
			}
		})
	}
}

func TestGitIndexMutationDisablesRepositoryHooksAndFsmonitor(t *testing.T) {
	c := fixture(t)
	write(t, c, "file.txt", "before\n")
	git(t, c, "add", "--", "file.txt")
	git(t, c, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
	write(t, c, "file.txt", "after\n")

	root := t.TempDir()
	fsmonitorMarker := filepath.Join(root, "fsmonitor-ran")
	fsmonitor := filepath.Join(root, "fsmonitor.sh")
	writeExecutable(t, fsmonitor, "#!/bin/sh\nprintf ran > '"+fsmonitorMarker+"'\nprintf '0\\n'\n")
	hookMarker := filepath.Join(root, "hook-ran")
	hooks := filepath.Join(root, "hooks")
	if err := os.Mkdir(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(hooks, "post-index-change"), "#!/bin/sh\nprintf ran > '"+hookMarker+"'\n")
	git(t, c, "config", "core.fsmonitor", fsmonitor)
	git(t, c, "config", "core.hooksPath", hooks)

	before := index(t, c)
	result, err := c.MutatePaths(t.Context(), "stage", "repo", []string{"file.txt"}, &before)
	if err != nil || result["index_sha256"] == before {
		t.Fatalf("stage = %#v, %v", result, err)
	}
	assertMarkerAbsent(t, fsmonitorMarker, hookMarker)

	after := result["index_sha256"].(string)
	if _, err = c.MutatePaths(t.Context(), "unstage", "repo", []string{"file.txt"}, &after); err != nil {
		t.Fatal(err)
	}
	assertMarkerAbsent(t, fsmonitorMarker, hookMarker)
}
