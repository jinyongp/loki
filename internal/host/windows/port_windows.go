//go:build windows

package windows

import (
	"errors"

	"golang.org/x/sys/windows"
)

func isSocketAddressInUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE)
}
