//go:build !windows

package windows

import (
	"errors"
	"syscall"
)

func isSocketAddressInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
