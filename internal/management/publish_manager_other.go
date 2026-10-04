//go:build !windows

package management

import "os"

func publishManagerFile(stage, executable string) error {
	return os.Rename(stage, executable)
}
