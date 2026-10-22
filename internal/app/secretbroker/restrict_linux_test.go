//go:build linux

package secretbroker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestUnixOnlyFilterCoversThreadsAndPreservesPrivateIPC(t *testing.T) {
	if os.Getenv("LOKI_TEST_UNIX_ONLY_PROCESS") == "1" {
		if err := RestrictUnixSockets(); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				for _, family := range []int{unix.AF_INET, unix.AF_INET6, unix.AF_NETLINK} {
					fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
					if fd >= 0 {
						unix.Close(fd)
					}
					if !errors.Is(err, unix.EAFNOSUPPORT) {
						t.Errorf("non-Unix family %d remained available: %v", family, err)
					}
				}
				fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
				if err != nil {
					t.Error("private IPC was rejected", err)
					return
				}
				defer unix.Close(fds[0])
				defer unix.Close(fds[1])
				if n, err := unix.Write(fds[0], []byte{42}); err != nil || n != 1 {
					t.Error("private IPC send failed")
				}
				var body [1]byte
				if n, err := unix.Read(fds[1], body[:]); err != nil || n != 1 || body[0] != 42 {
					t.Error("private IPC receive failed")
				}
			})
		}
		wg.Wait()
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, self, "-test.run=^TestUnixOnlyFilterCoversThreadsAndPreservesPrivateIPC$")
	command.Env = append(os.Environ(), "LOKI_TEST_UNIX_ONLY_PROCESS=1")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Unix-only process failed: %v %s", err, out)
	}
}
