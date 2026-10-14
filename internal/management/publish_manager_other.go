//go:build !windows

package management

import "os"

func managerBackupBusy(error) bool { return false }

func publishManagerFile(stage, executable string) error {
	return os.Rename(stage, executable)
}
