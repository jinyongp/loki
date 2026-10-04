package management

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishManagerWhileRunning(t *testing.T) {
	if ready := os.Getenv("LOKI_TEST_RUNNING_MANAGER_READY"); ready != "" {
		if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		return
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "loki.exe")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, old, 0700); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(directory, "ready")
	process := exec.Command(executable, "-test.run=^TestPublishManagerWhileRunning$")
	process.Env = append(os.Environ(), "LOKI_TEST_RUNNING_MANAGER_READY="+ready)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Process.Kill(); _ = process.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("running CLI did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stage := filepath.Join(directory, "candidate")
	if err := os.WriteFile(stage, []byte("new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := publishManagerFile(stage, executable); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(executable); err != nil || string(data) != "new" {
		t.Fatalf("new CLI: %q %v", data, err)
	}
	if info, err := os.Stat(stage + ".previous"); err != nil || info.Size() != int64(len(old)) {
		t.Fatalf("old CLI backup: %v %v", info, err)
	}
}

func TestPublishManagerRestoresOldCLIOnFailure(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "loki.exe")
	if err := os.WriteFile(executable, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := publishManagerFile(filepath.Join(directory, "missing-stage"), executable); err == nil {
		t.Fatal("missing candidate was accepted")
	}
	if data, err := os.ReadFile(executable); err != nil || string(data) != "old" {
		t.Fatalf("old CLI was not restored: %q %v", data, err)
	}
}
