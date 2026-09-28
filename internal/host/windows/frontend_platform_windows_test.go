//go:build windows

package windows

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFrontendPlatformReplacesRunningExecutable(t *testing.T) {
	if os.Getenv("LOKI_FRONTEND_REPLACE_HELPER") == "1" {
		return
	}
	root := t.TempDir()
	platform := NewWindowsFrontendPlatform()
	if err := platform.EnsurePrivateDirectory(t.Context(), root); err != nil {
		t.Fatal(err)
	}

	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "loki-running.exe")
	if err = copyTestExecutable(current, target); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "ready")
	command := exec.Command(target, "-test.run=TestFrontendReplacementHelper$")
	command.Env = append(os.Environ(),
		"LOKI_FRONTEND_REPLACE_HELPER=1",
		"LOKI_FRONTEND_REPLACE_READY="+ready,
	)
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(ready); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("running executable helper did not become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}

	source := filepath.Join(root, "replacement.exe")
	replacement := []byte("verified replacement frontend bytes")
	if err = os.WriteFile(source, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = platform.PublishExecutable(context.Background(), source, target); err != nil {
		t.Fatalf("replace running frontend: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(replacement) {
		t.Fatalf("replacement bytes=%q", got)
	}

	if err = command.Wait(); err != nil {
		t.Fatalf("running helper failed after replacement: %v", err)
	}
	command.Process = nil
	if err = cleanupPreviousExecutable(target); err != nil {
		t.Fatalf("cleanup previous frontend: %v", err)
	}
}

func TestFrontendReplacementHelper(t *testing.T) {
	if os.Getenv("LOKI_FRONTEND_REPLACE_HELPER") != "1" {
		return
	}
	ready := os.Getenv("LOKI_FRONTEND_REPLACE_READY")
	if ready == "" {
		t.Fatal("replacement helper ready path is missing")
	}
	if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
}

func copyTestExecutable(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err = io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err = output.Close(); err != nil {
		return err
	}
	return os.Chmod(target, 0o700)
}
