package policy

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeniedPaths(t *testing.T) {
	w, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, p := range []string{"/etc/passwd", "../escape", "a/../b", ".git/config", ".ENV", "private.PEM", "a\\b", "a\x00b", ".ssh/known_hosts", "x/.npmrc"} {
		if _, err := w.Resolve(p, false); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	if _, err := w.Resolve("nested/missing/file", false); err != nil {
		t.Fatal(err)
	}
}

func TestCWDAndSymlinks(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "repo"), 0700)
	w, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, cwd := range []string{"repo", "/workspace/repo"} {
		got, err := w.ResolveCWD(cwd)
		if err != nil || got != filepath.Join(root, "repo") {
			t.Fatalf("cwd %s: %s %v", cwd, got, err)
		}
	}
	for _, cwd := range []string{"/workspacex/repo", "/srv/workspace/loki/repo", "/workspace/../etc"} {
		if _, err := w.ResolveCWD(cwd); err == nil {
			t.Fatalf("accepted cwd %s", cwd)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Open("link", os.O_RDONLY, 0); err == nil {
		t.Fatal("followed outside symlink")
	}
	os.Symlink("repo", filepath.Join(root, "inside"))
	if _, err := w.Open("inside", os.O_RDONLY, 0); err == nil {
		t.Fatal("followed internal symlink")
	}
	os.Symlink("missing", filepath.Join(root, "broken"))
	if _, err := w.Resolve("broken", false); err == nil {
		t.Fatal("accepted broken symlink")
	}
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Open("pipe", os.O_RDONLY, 0); err == nil || !strings.Contains(err.Error(), "special") {
		t.Fatalf("special file: %v", err)
	}
}

func TestPinnedRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	os.Mkdir(root, 0700)
	os.WriteFile(filepath.Join(root, "file"), []byte("original"), 0600)
	w, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	os.Rename(root, filepath.Join(parent, "moved"))
	os.Mkdir(root, 0700)
	os.WriteFile(filepath.Join(root, "file"), []byte("replacement"), 0600)
	f, err := w.Open("file", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data := make([]byte, 8)
	f.Read(data)
	if string(data) != "original" {
		t.Fatal("root path swap changed pinned root")
	}
}
