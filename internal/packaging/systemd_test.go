package packaging

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"loki/internal/service"
)

func TestBundledSkillIsPinnedDevtoolsOnly(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "bundled_skills"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "devtools" || !entries[0].IsDir() {
		t.Fatalf("bundled skills = %#v", entries)
	}
	data, err := os.ReadFile(filepath.Join(root, "devtools", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(data)), "55a260c71fff25e7a731244bbf7043f055cadde26f5bc79fe4430ff23a0ea3bd"; got != want {
		t.Fatalf("devtools 0.8.2 skill digest = %s, want %s", got, want)
	}
}

func waitScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", "wait-for-loki-sockets.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLayoutRendererProducesServiceOwnedInputs(t *testing.T) {
	root := t.TempDir()
	templates, err := filepath.Abs(filepath.Join("..", "..", "packaging", "go"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "render-loki-go-layouts.sh"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(script, templates, root, "1001", "1002", "1003", "1004")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("render layouts: %v %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var runtime service.RuntimeOptions
	if err = json.Unmarshal(data, &runtime); err != nil {
		t.Fatal(err)
	}
	if runtime.RunnerUID != 1001 || runtime.RunnerGID != 1002 || runtime.SocketGID != 1003 {
		t.Fatalf("runtime identities = %#v", runtime)
	}
	if runtime.Socket != "/run/loki-go/runtime/control.sock" || runtime.StateDirectory != "/var/lib/loki-go/runtime" {
		t.Fatalf("runtime paths = %#v", runtime)
	}
	data, err = os.ReadFile(filepath.Join(root, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mcp map[string]any
	if err = json.Unmarshal(data, &mcp); err != nil {
		t.Fatal(err)
	}
	if mcp["PortGuardUID"] != float64(1001) || mcp["BrowserUID"] != float64(1004) {
		t.Fatalf("MCP identities = %#v", mcp)
	}
	if mcp["RuntimeSocket"] != "/run/loki-go/runtime/control.sock" {
		t.Fatalf("MCP runtime socket = %#v", mcp["RuntimeSocket"])
	}
	for _, name := range []string{"runtime.json", "mcp.json", "identity.env"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(name, err)
		}
		if info.Mode().Perm() != 0640 {
			t.Fatalf("%s mode = %v", name, info.Mode().Perm())
		}
	}
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
