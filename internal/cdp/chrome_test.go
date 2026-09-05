package cdp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestChromiumPipe(t *testing.T) {
	binary := os.Getenv("LOKI_TEST_CHROME")
	if binary == "" {
		if os.Getenv("LOKI_REQUIRE_BROWSER_TESTS") == "1" {
			t.Fatal("LOKI_TEST_CHROME is required")
		}
		t.Skip("optional development Chromium fixture")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputRead.Close()
	defer inputWrite.Close()
	defer outputRead.Close()
	defer outputWrite.Close()
	command := exec.Command(binary, "--headless", "--no-sandbox", "--disable-gpu", "--disable-background-networking", "--disable-sync", "--host-resolver-rules=MAP * ~NOTFOUND", "--remote-debugging-pipe", "--user-data-dir="+t.TempDir(), "about:blank")
	command.ExtraFiles = []*os.File{inputRead, outputWrite}
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LD_LIBRARY_PATH=" + os.Getenv("LOKI_TEST_CHROME_LIBS")}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL); _ = command.Wait() }()
	inputRead.Close()
	outputWrite.Close()
	client := New(outputRead, inputWrite, nil)
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var version struct{ Product, ProtocolVersion string }
	if err = client.Call(ctx, "", "Browser.getVersion", nil, &version); err != nil {
		t.Fatal(err)
	}
	if version.Product == "" || version.ProtocolVersion == "" {
		t.Fatal(version)
	}
	var target struct{ TargetID string }
	if err = client.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil || target.TargetID == "" {
		t.Fatal(target, err)
	}
	if err = client.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": target.TargetID}, nil); err != nil {
		t.Fatal(err)
	}
	t.Log(version.Product)
}
