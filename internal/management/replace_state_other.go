//go:build !windows

package management

import "os"

func replaceStateFile(source, destination string) error {
	return os.Rename(source, destination)
}
