//go:build linux

package secretbroker

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func runtimeFixture(t *testing.T) (string, RuntimeSocket) {
	t.Helper()
	root, err := os.MkdirTemp("/dev/shm", "loki-runtime-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if os.Chmod(root, 0700) != nil {
		t.Fatal("private runtime root")
	}
	dir := filepath.Join(root, "jobs")
	if os.Mkdir(dir, 0700) != nil || os.Chmod(dir, 0700) != nil {
		t.Fatal("private role directory")
	}
	return root, RuntimeSocket{Path: filepath.Join(dir, "jobs.sock"), Group: uint32(os.Getgid())}
}

func abandonedSocket(t *testing.T, path string) {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if os.Chmod(path, 0600) != nil {
		t.Fatal("private abandoned socket")
	}
	if listener.Close() != nil {
		t.Fatal("abandon socket")
	}
}

func TestRuntimeLeaseRejectsLiveAndUnknownSocketsAndRecoversOwnedStale(t *testing.T) {
	root, socket := runtimeFixture(t)
	sockets := []RuntimeSocket{socket}
	runner := "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2"
	first, err := AcquireRuntime(root, "deployment-fixture", runner, sockets)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := AcquireRuntime(root, "deployment-fixture", runner, sockets); err == nil {
		duplicate.Close()
		t.Fatal("two broker instances acquired one runtime")
	}
	listener, err := listenPrivate(socket.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if next, err := AcquireRuntime(root, "deployment-fixture", runner, sockets); err == nil {
		next.Close()
		t.Fatal("a live role endpoint was unlinked")
	}
	if _, err := os.Stat(socket.Path); err != nil {
		t.Fatal("live endpoint disappeared")
	}
	if listener.Close() != nil {
		t.Fatal("listener close")
	}
	abandonedSocket(t, socket.Path)
	if next, err := AcquireRuntime(root, "another-deployment", runner, sockets); err == nil {
		next.Close()
		t.Fatal("a different deployment adopted runtime ownership")
	}
	if _, err := os.Stat(socket.Path); err != nil {
		t.Fatal("unknown endpoint was deleted")
	}
	next, err := AcquireRuntime(root, "deployment-fixture", runner, sockets)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next.InstanceID == first.InstanceID || !uuidPattern.MatchString(next.InstanceID) {
		t.Fatal("receiver instance identity was reused")
	}
	if _, err := os.Stat(socket.Path); !os.IsNotExist(err) {
		t.Fatal("owned stale endpoint survived")
	}
}

func TestRuntimeLeaseCannotClassifyAnUnmarkedOrSymlinkEndpoint(t *testing.T) {
	root, socket := runtimeFixture(t)
	abandonedSocket(t, socket.Path)
	runner := "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2"
	if lease, err := AcquireRuntime(root, "fixture", runner, []RuntimeSocket{socket}); err == nil {
		lease.Close()
		t.Fatal("unmarked endpoint was adopted")
	}
	if os.Remove(socket.Path) != nil {
		t.Fatal("remove fixture orphan")
	}
	lease, err := AcquireRuntime(root, "fixture", runner, []RuntimeSocket{socket})
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	target := filepath.Join(root, "unrelated")
	if os.WriteFile(target, []byte("retained"), 0600) != nil || os.Symlink(target, socket.Path) != nil {
		t.Fatal("symlink fixture")
	}
	if lease, err := AcquireRuntime(root, "fixture", runner, []RuntimeSocket{socket}); err == nil {
		lease.Close()
		t.Fatal("symlink endpoint was adopted")
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "retained" {
		t.Fatal("unrelated file changed")
	}
}
