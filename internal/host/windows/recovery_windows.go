//go:build windows

package windows

import "os"

type OSPathRemover struct{}

func (OSPathRemover) RemoveAll(path string) error {
	return os.RemoveAll(path)
}
