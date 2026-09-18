package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func withUmask(t *testing.T, mask int, fn func()) {
	t.Helper()
	previous := syscall.Umask(mask)
	defer syscall.Umask(previous)
	fn()
}

func privateMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file", path)
	}
	return info.Mode().Perm()
}

func assertNoPrivateTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".loki-private-") {
			t.Fatalf("temporary publication file remained: %s", entry.Name())
		}
	}
}

func TestPublishPrivateCreateAndOverwrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		mask int
	}{
		{name: "umask-0022", mask: 0o022},
		{name: "umask-0077", mask: 0o077},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state")
			withUmask(t, tc.mask, func() {
				if err := PublishPrivate(path, []byte("old"), false); err != nil {
					t.Fatal(err)
				}
				if got := privateMode(t, path); got != 0o600 {
					t.Fatalf("create mode = %o, want 600", got)
				}
				if err := PublishPrivate(path, []byte("new"), true); err != nil {
					t.Fatal(err)
				}
			})
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "new" {
				t.Fatalf("overwrite content = %q", data)
			}
			if got := privateMode(t, path); got != 0o600 {
				t.Fatalf("overwrite mode = %o, want 600", got)
			}
			assertNoPrivateTemps(t, dir)
		})
	}
}

func TestPublishPrivateNoOverwritePreservesExistingAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishPrivate(path, []byte("new"), false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("no-overwrite error = %v, want exists", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("existing content changed to %q", data)
	}
	assertNoPrivateTemps(t, dir)
}
