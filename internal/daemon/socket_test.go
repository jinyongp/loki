package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnedPrivateDirectoryAcceptsExactIdentityAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := OwnedPrivateDirectory(path, uint32(os.Getuid()), uint32(os.Getgid())); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "state")
	if err := os.WriteFile(marker, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := OwnedPrivateDirectory(path, uint32(os.Getuid()), uint32(os.Getgid())); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "preserved" {
		t.Fatalf("runner state = %q, %v", raw, err)
	}
}

func TestOwnedPrivateDirectoryRejectsUnsafeLayouts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runner")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func() error{
		"mode": func() error { return os.Chmod(path, 0750) },
		"uid":  func() error { return nil },
	} {
		if err := change(); err != nil {
			t.Fatal(err)
		}
		uid := uint32(os.Getuid())
		if name == "uid" {
			uid++
		}
		if err := OwnedPrivateDirectory(path, uid, uint32(os.Getgid())); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("%s layout error = %v", name, err)
		}
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := OwnedPrivateDirectory(link, uint32(os.Getuid()), uint32(os.Getgid())); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink layout error = %v", err)
	}
}
