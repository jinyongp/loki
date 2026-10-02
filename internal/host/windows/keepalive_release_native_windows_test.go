//go:build windows

package windows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsKeepaliveExecutableRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loki-keepalive.exe")
	if err := os.WriteFile(path, []byte("locked image"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- waitForKeepaliveExecutableRelease(ctx, path) }()
	select {
	case err := <-result:
		t.Fatalf("locked executable was treated as released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = 0
	if err := <-result; err != nil {
		t.Fatalf("executable remained blocked after release: %v", err)
	}
	if err := waitForKeepaliveExecutableRelease(ctx, path+".missing"); err != nil {
		t.Fatalf("missing companion prevented cleanup: %v", err)
	}
	if err := waitForKeepaliveExecutableRelease(ctx, filepath.Join(filepath.Dir(path), "missing-directory", "loki-keepalive.exe")); err != nil {
		t.Fatalf("missing companion directory prevented cleanup: %v", err)
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if err := waitForKeepaliveExecutableRelease(canceled, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was ignored: %v", err)
	}
}
