//go:build windows

package windows

import (
	"errors"
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsSocketAddressInUseClassification(t *testing.T) {
	wrapped := &net.OpError{Op: "listen", Net: "tcp4", Err: &os.SyscallError{
		Syscall: "bind", Err: windows.WSAEADDRINUSE,
	}}
	if !isSocketAddressInUse(wrapped) {
		t.Fatal("wrapped Windows address-in-use error was not recognized")
	}
	for _, err := range []error{nil, windows.WSAEACCES, errors.New("bind failure")} {
		if isSocketAddressInUse(err) {
			t.Fatalf("non-conflict error was swallowed: %v", err)
		}
	}
}
