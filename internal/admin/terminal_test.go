package admin

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type promptReady chan struct{}

func (p promptReady) Write(data []byte) (int, error) {
	select {
	case p <- struct{}{}:
	default:
	}
	return len(data), nil
}

func TestSecretTerminalEchoAndCancellation(t *testing.T) {
	for _, cancelInput := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelInput), func(t *testing.T) {
			fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			master := os.NewFile(uintptr(fd), "pty master")
			defer master.Close()
			if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
				t.Fatal(err)
			}
			number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
			if err != nil {
				t.Fatal(err)
			}
			slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer slave.Close()
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			ready := make(promptReady, 1)
			done := make(chan error, 1)
			go func() {
				value, err := readTerminal(ctx, slave, ready)
				if !cancelInput && (err != nil || value != "synthetic-한글") {
					done <- fmt.Errorf("terminal input mismatch")
					return
				}
				if cancelInput && err == nil {
					done <- fmt.Errorf("cancellation ignored")
					return
				}
				done <- nil
			}()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			hidden, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || hidden.Lflag&unix.ECHO != 0 {
				t.Fatal("input echo enabled")
			}
			if cancelInput {
				cancel()
			} else {
				io.WriteString(master, "synthetic-한긁\x7f글\n")
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("terminal settings not restored")
			}
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if n, err := unix.Poll(poll, 20); err != nil || n != 0 {
				t.Fatal("secret bytes echoed to terminal")
			}
		})
	}
}
