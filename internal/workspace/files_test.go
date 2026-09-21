package workspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"loki/internal/config"
	"loki/internal/fault"
	"loki/internal/gitops"
	"loki/internal/process"
)

type workspaceTestGitRunner struct {
	root string
	env  []string
}

func (r workspaceTestGitRunner) Run(ctx context.Context, request gitops.CommandRequest) (gitops.CommandResult, error) {
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

func fixture(t *testing.T) *Files {
	t.Helper()
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	c.MaxFileBytes = 1 << 20
	c.MaxWriteBytes = 1 << 20
	c.MaxOutputBytes = 64 << 10
	c.MaxListEntries = 100
	c.MaxSearchResults = 20
	c.MaxReadLines = 100
	c.MaxPatchBytes = 64 << 10
	c.MaxPatchFiles = 10
	f, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	environment := []string{
		"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0",
	}
	f.GitRunner = workspaceTestGitRunner{root: f.Policy.Root(), env: environment}
	if _, err = os.Stat(f.RGPath); err != nil {
		f.RGPath = "/home/linuxbrew/.linuxbrew/bin/rg"
	}
	return f
}

func TestFileLifecycleAndDirtyRestore(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	if _, err := f.Create("docs/note.txt", "alpha\nbeta\ngamma\n"); err != nil {
		t.Fatal(err)
	}
	chunk, err := f.Read("docs/note.txt", 1, 1)
	if err != nil || chunk["content"] != "beta\n" || chunk["eof"] != false || chunk["next_offset"] != 2 {
		t.Fatalf("chunk %v %v", chunk, err)
	}
	search, err := f.Search(ctx, "gamma", ".", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	matches := search["matches"].([]map[string]any)
	if len(matches) != 1 || matches[0]["line"] != 3 {
		t.Fatalf("search=%v", search)
	}
	before, _ := f.Read("docs/note.txt", 0, 100)
	replaced, err := f.Replace("docs/note.txt", "beta", "delta", before["sha256"].(string), 1)
	if err != nil {
		t.Fatal(err)
	}
	revision := replaced["previous_revision"].(string)
	revisions, err := f.Revisions("docs/note.txt", 20)
	if err != nil {
		t.Fatal(err)
	}
	if revisions["revisions"].([]Revision)[0].Revision != revision {
		t.Fatal("missing preimage")
	}
	diff, err := f.RevisionDiff("docs/note.txt", revision)
	if err != nil || !strings.Contains(diff["diff"].(string), "-beta\n+delta") {
		t.Fatalf("diff %v %v", diff, err)
	}
	if _, err = f.Replace("docs/note.txt", "delta", "stale", before["sha256"].(string), 1); err == nil {
		t.Fatal("accepted stale hash")
	}
	moveSource, err := f.Read("docs/note.txt", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Move("docs/note.txt", "docs/moved.txt", before["sha256"].(string), "missing"); err == nil {
		t.Fatal("accepted stale move source hash")
	}
	if _, err = f.Move("docs/note.txt", "docs/moved.txt", moveSource["sha256"].(string), "present"); err == nil {
		t.Fatal("accepted invalid destination precondition")
	}
	if _, err = f.Move("docs/note.txt", "docs/moved.txt", moveSource["sha256"].(string), "missing"); err != nil {
		t.Fatal(err)
	}
	restored, err := f.Restore("docs/note.txt", revision, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if restored["undo_revision"] != nil {
		t.Fatal("missing restore unexpectedly had preimage")
	}
	after, _ := f.Read("docs/note.txt", 0, 100)
	if after["content"] != before["content"] {
		t.Fatal("restore changed original content")
	}
	os.Chmod(filepath.Join(f.Policy.Root(), "docs/note.txt"), 0755)
	changed, err := f.Replace("docs/note.txt", "beta", "new", after["sha256"].(string), 1)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(f.Policy.Root(), "docs/note.txt"))
	if info.Mode().Perm() != 0755 {
		t.Fatal("replace lost mode")
	}
	if _, err = f.Restore("docs/note.txt", changed["previous_revision"].(string), before["sha256"].(string)); err == nil {
		t.Fatal("accepted stale restore hash")
	} else if detail := fault.Describe(err); detail.Code != fault.CodeConflict {
		t.Fatalf("stale restore detail = %#v", detail)
	}
	result, err := f.Restore("docs/note.txt", changed["previous_revision"].(string), changed["sha256"].(string))
	if err != nil || result["undo_revision"] == nil {
		t.Fatalf("restore %v %v", result, err)
	}
	info, _ = os.Stat(filepath.Join(f.Policy.Root(), "docs/note.txt"))
	if info.Mode().Perm() != 0755 {
		t.Fatal("restore lost mode")
	}
}

func TestFileLimitsPolicyAndPagination(t *testing.T) {
	f := fixture(t)
	for _, path := range []string{".env", ".git/config", "../outside", "link/file"} {
		if path == "link/file" {
			os.Symlink(t.TempDir(), filepath.Join(f.Policy.Root(), "link"))
		}
		if _, err := f.Create(path, "secret"); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	f.Create("user.txt", "user change\n")
	if _, err := f.Create("user.txt", "replacement"); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	f.Create("dir/a.txt", "a")
	f.Create("dir/b.txt", "b")
	f.Create("z.txt", "z")
	listing, err := f.List(context.Background(), ".", 3, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	entries := listing["entries"].([]map[string]any)
	if entries[0]["path"] != "dir" || entries[1]["path"] != "user.txt" || listing["has_more"] != true {
		t.Fatalf("order/pagination: %v", listing)
	}
	listing, err = f.List(context.Background(), ".", 3, 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing["entries"].([]map[string]any)) != 3 {
		t.Fatal(listing)
	}
	os.WriteFile(filepath.Join(f.Policy.Root(), "binary"), []byte{1, 0, 2}, 0600)
	if _, err = f.Read("binary", 0, 10); err == nil {
		t.Fatal("accepted binary")
	}
	os.WriteFile(filepath.Join(f.Policy.Root(), "invalid"), []byte{255}, 0600)
	if _, err = f.Read("invalid", 0, 10); err == nil {
		t.Fatal("accepted invalid utf8")
	}
	f.Config.MaxOutputBytes = 3
	f.Create("long.txt", "long-line")
	if _, err = f.Read("long.txt", 0, 1); err == nil {
		t.Fatal("unbounded line")
	}
	if got := Lines("one\r\ntwo\rthree\u2028end\n"); !reflect.DeepEqual(got, []string{"one\r\n", "two\r", "three\u2028", "end\n"}) {
		t.Fatalf("splitlines %q", got)
	}
}

func TestPatchTrackedDeleteAndRecovery(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	f.Create("dirty.txt", "before\n")
	patch := "--- a/dirty.txt\n+++ b/dirty.txt\n@@ -1 +1 @@\n-before\n+after\n"
	applied, err := f.Patch(ctx, patch)
	if err != nil {
		t.Fatal(err)
	}
	revision := applied["previous_revisions"].(map[string]string)["dirty.txt"]
	if revision == "" {
		t.Fatal("patch missing recovery")
	}
	for _, invalid := range []string{strings.ReplaceAll(patch, "dirty.txt", ".env"), "diff --git a/dirty.txt b/dirty.txt\ndeleted file mode 100644\n", strings.ReplaceAll(patch, "dirty.txt", "../outside")} {
		if _, err := f.Patch(ctx, invalid); err == nil {
			t.Fatalf("accepted unsafe patch %q", invalid)
		}
	}
	if _, err = f.RemoveTracked(ctx, "dirty.txt", Digest([]byte("after\n"))); err == nil {
		t.Fatal("removed untracked file")
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "dirty.txt"}} {
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Dir = f.Policy.Root()
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, output, err)
		}
	}
	observed := Digest([]byte("after\n"))
	if err = os.WriteFile(filepath.Join(f.Policy.Root(), "dirty.txt"), []byte("changed-after-read\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.RemoveTracked(ctx, "dirty.txt", observed); err == nil || !strings.Contains(err.Error(), "file changed since it was read") {
		t.Fatalf("stale removal error = %v", err)
	} else if detail := fault.Describe(err); detail.Code != fault.CodeConflict {
		t.Fatalf("stale removal detail = %#v", detail)
	}
	if _, err = os.Stat(filepath.Join(f.Policy.Root(), "dirty.txt")); err != nil {
		t.Fatalf("stale removal changed file: %v", err)
	}
	if err = os.WriteFile(filepath.Join(f.Policy.Root(), "dirty.txt"), []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	removed, err := f.RemoveTracked(ctx, "dirty.txt", observed)
	if err != nil || removed["previous_revision"] == nil || removed["previous_sha256"] != observed {
		t.Fatalf("remove %v %v", removed, err)
	}
	if _, err = f.Restore("dirty.txt", revision, "missing"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(f.Policy.Root(), "dirty.txt"))
	if string(data) != "before\n" {
		t.Fatal("recovery changed preimage")
	}
}

func TestImageWriteReadAndGuard(t *testing.T) {
	f := fixture(t)
	image := []byte("\x89PNG\r\n\x1a\ndata")
	encoded := base64.StdEncoding.EncodeToString(image)
	created, err := f.WriteImage("images/result.png", encoded, "image/png", false, "")
	if err != nil {
		t.Fatal(err)
	}
	data, metadata, err := f.Image("images/result.png")
	if err != nil || string(data) != string(image) || metadata["sha256"] != "0a8658df1c970c052938fa0e8fe369a6351fb74b51b9f399a72397ed8c94b3ba" {
		t.Fatalf("image: %v %v", metadata, err)
	}
	if created["previous_revision"] != nil {
		t.Fatal("unexpected revision")
	}
	if _, err = f.WriteImage("images/result.png", encoded, "image/png", false, ""); !errors.Is(err, os.ErrExist) {
		t.Fatal("overwrote image")
	}
	if _, err = f.WriteImage("images/result.png", encoded, "image/png", true, "stale"); err == nil {
		t.Fatal("accepted stale image")
	}
	updated, err := f.WriteImage("images/result.png", encoded, "image/png", true, metadata["sha256"].(string))
	if err != nil || updated["previous_revision"] == nil {
		t.Fatalf("image replacement %v %v", updated, err)
	}
	for _, args := range [][3]string{{"x.jpg", encoded, "image/png"}, {"x.png", "!!!!", "image/png"}, {"x.png", base64.StdEncoding.EncodeToString([]byte("bad")), "image/png"}, {"x.svg", encoded, "image/svg+xml"}} {
		if _, err = f.WriteImage(args[0], args[1], args[2], false, ""); err == nil {
			t.Fatal("invalid image accepted")
		}
	}
}

func TestConcurrentOptimisticEdit(t *testing.T) {
	f := fixture(t)
	f.Create("shared.txt", "initial")
	before, _ := f.Read("shared.txt", 0, 10)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, err := f.Replace("shared.txt", "initial", "changed", before["sha256"].(string), 1); err == nil {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("%d editors won", successes.Load())
	}
}
