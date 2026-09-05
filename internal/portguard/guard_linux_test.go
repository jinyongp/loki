package portguard

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestPortHelper(t *testing.T) {
	if os.Getenv("LOKI_PORT_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	fmt.Println(listener.Addr().(*net.TCPAddr).Port)
	for {
		conn, err := listener.Accept()
		if err != nil {
			os.Exit(3)
		}
		conn.Close()
	}
}
func TestInspectAndStopOwnedListener(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPortHelper$")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "LOKI_PORT_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	g := &Guard{Root: root, UID: uint32(os.Getuid())}
	result, err := g.Inspect(t.Context(), port)
	if err != nil {
		t.Fatal(err)
	}
	rows := result["listeners"].([]map[string]any)
	if result["in_use"] != true || len(rows) != 1 || rows[0]["pid"] != cmd.Process.Pid || rows[0]["cwd"] != "/workspace" {
		t.Fatal(result)
	}
	other := &Guard{Root: t.TempDir(), UID: uint32(os.Getuid())}
	if _, err = other.Inspect(t.Context(), port); err == nil {
		t.Fatal("outside workspace listener accepted")
	}
	if _, err = other.Stop(t.Context(), port); err == nil {
		t.Fatal("outside workspace stop accepted")
	}
	result, err = g.Stop(t.Context(), port)
	if err != nil {
		t.Fatal(err)
	}
	if result["stopped"] != true {
		t.Fatal(result)
	}
	result, err = g.Inspect(t.Context(), port)
	if err != nil || result["in_use"] != false {
		t.Fatal(result, err)
	}
}
func TestBoundary(t *testing.T) {
	for _, port := range []int{0, 1023, 65536, 8765, 8766, 8767} {
		if Validate(port) == nil {
			t.Fatal(port)
		}
	}
	for _, path := range []string{"/workspace-other", "/workspace/../etc"} {
		if within("/workspace", path) {
			t.Fatal(path)
		}
	}
	for _, path := range []string{"/system.slice/loki-action-deadbeefdeadbeef.scope", "/system.slice/loki-project-bootstrap-deadbeefdeadbeef.scope"} {
		if !scopePattern.MatchString(path) {
			t.Fatal(path)
		}
	}
	if scopePattern.MatchString("/system.slice/loki-action-evil.scope") {
		t.Fatal("scope")
	}
}
