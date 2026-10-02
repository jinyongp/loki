package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestReleaseTagPublicationReconcilesTransientFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tag publication runs on the Linux release runner")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "maintainer", "publish-tag.sh"))
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		pass   bool
		pushes int
	}{
		{"fresh", true, 1}, {"existing", true, 0},
		{"transient-push", true, 2}, {"transient-lookup", true, 1},
		{"ambiguous-push", true, 1}, {"exhausted", false, 3},
		{"permission", false, 1}, {"lookup-denied", false, 0},
		{"permission-with-transient", false, 1},
		{"remote-conflict", false, 0}, {"local-conflict", false, 0},
		{"concurrent-conflict", false, 1}, {"unknown", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			remote := filepath.Join(root, "origin.git")
			checkout := filepath.Join(root, "checkout")
			bin := filepath.Join(root, "bin")
			for _, dir := range []string{checkout, bin} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			git := func(dir string, args ...string) string {
				t.Helper()
				cmd := exec.Command(realGit, args...)
				cmd.Dir = dir
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, output)
				}
				return strings.TrimSpace(string(output))
			}
			git(root, "init", "--bare", remote)
			git(checkout, "init")
			git(checkout, "config", "core.hooksPath", filepath.Join(root, "no-hooks"))
			git(checkout, "config", "tag.gpgSign", "false")
			git(checkout, "remote", "add", "origin", remote)
			git(checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "accepted")
			accepted := git(checkout, "rev-parse", "HEAD")
			git(checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "other")
			other := git(checkout, "rev-parse", "HEAD")
			git(checkout, "push", "origin", "HEAD:refs/heads/main")
			ref := "refs/tags/v0.0.1"
			switch tc.name {
			case "existing":
				git(root, "--git-dir", remote, "update-ref", ref, accepted)
			case "remote-conflict":
				git(root, "--git-dir", remote, "update-ref", ref, other)
			case "local-conflict":
				git(checkout, "tag", "v0.0.1", other)
			}
			fake := `#!/bin/sh
set -eu
case "$1" in
  ls-remote)
    count=0
    if test -f "$LOKI_TAG_TEST_ROOT/lookups"; then count=$(cat "$LOKI_TAG_TEST_ROOT/lookups"); fi
    count=$((count + 1))
    echo "$count" > "$LOKI_TAG_TEST_ROOT/lookups"
    if test "$LOKI_TAG_TEST_MODE" = transient-lookup && test "$count" -eq 1; then
      echo 'fatal: The requested URL returned error: 503' >&2; exit 1
    fi
    if test "$LOKI_TAG_TEST_MODE" = lookup-denied; then
      echo 'fatal: Authentication failed' >&2; exit 1
    fi
    ;;
  push)
    count=0
    if test -f "$LOKI_TAG_TEST_ROOT/pushes"; then count=$(cat "$LOKI_TAG_TEST_ROOT/pushes"); fi
    count=$((count + 1))
    echo "$count" > "$LOKI_TAG_TEST_ROOT/pushes"
    test "$#" -eq 3 && test "$2" = origin && test "$3" = refs/tags/v0.0.1
    case "$LOKI_TAG_TEST_MODE" in
      transient-push) if test "$count" -eq 1; then echo 'remote: fatal error in commit_refs' >&2; exit 1; fi ;;
      exhausted) echo 'remote: fatal error in commit_refs' >&2; exit 1 ;;
      ambiguous-push) "$LOKI_TAG_TEST_GIT" "$@"; echo 'remote: fatal error in commit_refs' >&2; exit 1 ;;
      permission) echo 'remote: Permission denied (403)' >&2; exit 1 ;;
      permission-with-transient) echo 'remote: Permission denied (403). remote: fatal error in commit_refs' >&2; exit 1 ;;
      unknown) echo 'remote: unclassified rejection' >&2; exit 1 ;;
      concurrent-conflict)
        "$LOKI_TAG_TEST_GIT" --git-dir "$LOKI_TAG_TEST_REMOTE" update-ref refs/tags/v0.0.1 "$LOKI_TAG_TEST_OTHER"
        echo 'remote: fatal error in commit_refs' >&2; exit 1 ;;
    esac
    ;;
esac
exec "$LOKI_TAG_TEST_GIT" "$@"
`
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", script, "v0.0.1", accepted)
			cmd.Dir = checkout
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"LOKI_TAG_RETRY_DELAY_SECONDS=0", "LOKI_TAG_TEST_GIT="+realGit,
				"LOKI_TAG_TEST_ROOT="+root, "LOKI_TAG_TEST_MODE="+tc.name,
				"LOKI_TAG_TEST_REMOTE="+remote, "LOKI_TAG_TEST_OTHER="+other)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.pass {
				t.Fatalf("pass=%v want=%v: %v\n%s", err == nil, tc.pass, err, output)
			}
			pushes := 0
			if raw, err := os.ReadFile(filepath.Join(root, "pushes")); err == nil {
				pushes, err = strconv.Atoi(strings.TrimSpace(string(raw)))
				if err != nil {
					t.Fatal(err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if pushes != tc.pushes {
				t.Fatalf("pushes=%d want=%d\n%s", pushes, tc.pushes, output)
			}
			if tc.pass && git(root, "--git-dir", remote, "rev-parse", ref) != accepted {
				t.Fatal("published tag differs from the accepted commit")
			}
			if (tc.name == "remote-conflict" || tc.name == "concurrent-conflict") && git(root, "--git-dir", remote, "rev-parse", ref) != other {
				t.Fatal("conflicting remote tag was changed")
			}
		})
	}
}
