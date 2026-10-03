package signing

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAgentLifecycle(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "key")
	if out, err := exec.Command("/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	public := filepath.Join(root, "public.sock")
	private := filepath.Join(root, "private.sock")
	// Hard shutdowns leave both socket inodes in the persistent Docker volume.
	for _, path := range []string{private, public} {
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		listener.SetUnlinkOnClose(false)
		if err = listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunAgent(ctx, AgentOptions{PrivateSocket: private, PublicSocket: public, Key: key, Grant: NewSSHSignatureGrant(uint32(os.Getuid())), SocketUID: os.Getuid(), SocketGID: os.Getgid(), Ready: func() error { close(ready); return nil }})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("readiness timeout")
	}
	if err := RunAgent(t.Context(), AgentOptions{PrivateSocket: private, PublicSocket: public, Key: key, Grant: NewSSHSignatureGrant(uint32(os.Getuid())), SocketUID: os.Getuid(), SocketGID: os.Getgid()}); err == nil {
		t.Fatal("concurrent signing agent accepted")
	}
	var stat unix.Stat_t
	if err := unix.Stat(public, &stat); err != nil {
		t.Fatal(err)
	}
	if int(stat.Uid) != os.Getuid() || int(stat.Gid) != os.Getgid() {
		t.Fatalf("public signing socket owner=%d:%d", stat.Uid, stat.Gid)
	}
	if stat.Mode&0777 != 0660 {
		t.Fatalf("public signing socket mode=%#o", stat.Mode&0777)
	}
	var parent unix.Stat_t
	if err := unix.Stat(root, &parent); err != nil {
		t.Fatal(err)
	}
	if parent.Mode&0777 != 0711 {
		t.Fatalf("signing socket directory mode=%#o", parent.Mode&0777)
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

func TestAgentSocketRecoveryPreservesOccupiedPaths(t *testing.T) {
	for _, kind := range []string{"live-socket", "file", "symlink", "wrong-owner"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "public.sock")
			uid := os.Getuid()
			switch kind {
			case "live-socket", "wrong-owner":
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				t.Cleanup(func() { _ = listener.Close() })
				if kind == "wrong-owner" {
					// Probe as a private socket: it must belong to the service.
					if os.Getuid() != 0 {
						t.Skip("foreign socket ownership fixture requires root")
					}
					if err = os.Chown(path, 12345, os.Getgid()); err != nil {
						t.Fatal(err)
					}
				}
			case "file":
				if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(root, "target"), path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			options := AgentOptions{PrivateSocket: filepath.Join(root, "private.sock"), PublicSocket: path, SocketUID: uid}
			if kind == "wrong-owner" {
				options.PrivateSocket, options.PublicSocket = path, options.PrivateSocket
			}
			if err = reclaimAgentSockets(t.Context(), options); err == nil {
				t.Fatal("occupied socket path accepted")
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("occupied socket path modified", err)
			}
		})
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
	err := RunAgent(t.Context(), AgentOptions{PrivateSocket: private, PublicSocket: public, Key: key, Grant: NewSSHSignatureGrant(uint32(os.Getuid())), SocketUID: os.Getuid(), SocketGID: os.Getgid(), Ready: func() error { return want }})
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
	err = RunAgent(t.Context(), AgentOptions{PrivateSocket: private, PublicSocket: public, Key: key, Grant: NewSSHSignatureGrant(uint32(os.Getuid())), SocketUID: os.Getuid(), SocketGID: os.Getgid()})
	if err == nil {
		t.Fatal("occupied public socket accepted")
	}
	data, err := os.ReadFile(public)
	if err != nil || string(data) != "preserve" {
		t.Fatal("existing path modified", err)
	}
}
