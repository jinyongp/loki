package signing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentLifecycle(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "key")
	if out, err := exec.Command("/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	public := filepath.Join(root, "public.sock")
	private := filepath.Join(root, "private.sock")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunAgent(ctx, AgentOptions{PrivateSocket: private, PublicSocket: public, Key: key, RunnerUID: uint32(os.Getuid()), SocketGID: os.Getgid(), Ready: func() error { close(ready); return nil }})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("readiness timeout")
	}
	list := exec.Command("/usr/bin/ssh-add", "-L")
	list.Env = []string{"SSH_AUTH_SOCK=" + public}
	out, err := list.CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "ssh-ed25519 ") {
		t.Fatalf("%s %v", out, err)
	}
	remove := exec.Command("/usr/bin/ssh-add", "-D")
	remove.Env = list.Env
	if err = remove.Run(); err == nil {
		t.Fatal("public proxy permitted key removal")
	}
	check := exec.Command("/usr/bin/ssh-add", "-L")
	check.Env = list.Env
	if out, err = check.Output(); err != nil || !strings.HasPrefix(string(out), "ssh-ed25519 ") {
		t.Fatal("key removed", err)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
	for _, path := range []string{public, private} {
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("socket retained", path, err)
		}
	}
}

func TestAgentStartupFailureCleanup(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "key")
	if out, err := exec.Command("/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	private, public := filepath.Join(root, "private.sock"), filepath.Join(root, "public.sock")
	want := errors.New("notification fixture failed")
	err := RunAgent(t.Context(), AgentOptions{PrivateSocket: private, PublicSocket: public, Key: key, RunnerUID: uint32(os.Getuid()), SocketGID: os.Getgid(), Ready: func() error { return want }})
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	for _, path := range []string{private, public} {
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal(path, err)
		}
	}
	if err = os.WriteFile(public, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	err = RunAgent(t.Context(), AgentOptions{PrivateSocket: private, PublicSocket: public, Key: key, RunnerUID: uint32(os.Getuid()), SocketGID: os.Getgid()})
	if err == nil {
		t.Fatal("occupied public socket accepted")
	}
	data, err := os.ReadFile(public)
	if err != nil || string(data) != "preserve" {
		t.Fatal("existing path modified", err)
	}
}
