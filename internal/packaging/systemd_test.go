package packaging

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func waitScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", "wait-for-loki-sockets.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSocketWaitHandlesDelayedDependencies(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"runtime", "port-guard", "browser", "signing"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(waitScript(t))
	command.Env = append(os.Environ(), "LOKI_RUN_ROOT="+root, "LOKI_SOCKET_ATTEMPTS=100", "LOKI_SOCKET_INTERVAL=0.01")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	listeners := []net.Listener{}
	for _, socket := range []string{"runtime/control.sock", "port-guard/control.sock", "browser/control.sock", "signing/agent.sock"} {
		listener, err := net.Listen("unix", filepath.Join(root, socket))
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
	}
	defer func() {
		for _, listener := range listeners {
			listener.Close()
		}
	}()
	if err := command.Wait(); err != nil {
		t.Fatal("wait helper rejected delayed sockets:", err)
	}
}

func TestSocketWaitIsBounded(t *testing.T) {
	command := exec.Command(waitScript(t))
	command.Env = append(os.Environ(), "LOKI_RUN_ROOT="+t.TempDir(), "LOKI_SOCKET_ATTEMPTS=2", "LOKI_SOCKET_INTERVAL=0.01")
	started := time.Now()
	if err := command.Run(); err == nil {
		t.Fatal("wait helper accepted missing sockets")
	}
	if time.Since(started) > time.Second {
		t.Fatal("wait helper exceeded its bounded retry interval")
	}
}
