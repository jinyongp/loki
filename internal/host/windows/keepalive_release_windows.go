//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// Task Scheduler can report Ready before Windows releases the executable image.
// Probe write/delete access before replacing or removing an owned companion.
func waitForKeepaliveExecutableRelease(ctx context.Context, path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 40; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		handle, openErr := windows.CreateFile(name, windows.GENERIC_WRITE|windows.DELETE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if openErr == nil {
			return windows.CloseHandle(handle)
		}
		if errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		if !errors.Is(openErr, windows.ERROR_ACCESS_DENIED) && !errors.Is(openErr, windows.ERROR_SHARING_VIOLATION) {
			return openErr
		}
		err = openErr
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("owned WSL keepalive executable is still in use after task stop: %w", err)
}
