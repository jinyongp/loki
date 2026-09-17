package daemon

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeSocketDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0750); err != nil {
		t.Fatal(err)
	}
	// The service contract requires group traversal on a pre-existing socket
	// directory; make the fixture independent of the runner's ambient umask.
	if err := os.Chmod(path, 0750); err != nil {
		t.Fatal(err)
	}
}

func TestListenReplacesOwnedStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service", "control.sock")
	makeSocketDirectory(t, filepath.Dir(path))
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err = stale.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(path, os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket retained after close", err)
	}
}

func TestListenRejectsActiveAndNonSocketPaths(t *testing.T) {
	root := t.TempDir()
	activePath := filepath.Join(root, "active", "control.sock")
	makeSocketDirectory(t, filepath.Dir(activePath))
	active, err := net.ListenUnix("unix", &net.UnixAddr{Name: activePath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	if listener, listenErr := Listen(activePath, os.Getgid()); listenErr == nil {
		listener.Close()
		t.Fatal("active service socket replaced")
	}

	filePath := filepath.Join(root, "file", "control.sock")
	makeSocketDirectory(t, filepath.Dir(filePath))
	if err = os.WriteFile(filePath, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	if listener, listenErr := Listen(filePath, os.Getgid()); listenErr == nil {
		listener.Close()
		t.Fatal("non-socket path replaced")
	}
}

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
