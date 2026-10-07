//go:build !windows

package management

import (
	"os"
)

func protectManagementRoot(root string) error { return os.Chmod(root, 0700) }
