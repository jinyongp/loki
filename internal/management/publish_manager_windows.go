package management

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

func managerBackupBusy(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// A running Windows executable can be renamed, but cannot be overwritten.
// Its journal-owned backup remains recoverable until the old process exits.
func publishManagerFile(stage, executable string) error {
	backup := stage + ".previous"
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("manager backup path already exists or is unavailable")
	}
	hadPrevious := false
	if err := os.Rename(executable, backup); err == nil {
		hadPrevious = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, executable); err != nil {
		if hadPrevious {
			if restoreErr := os.Rename(backup, executable); restoreErr != nil {
				return fmt.Errorf("publication failed: %v; restoring the old CLI failed: %w", err, restoreErr)
			}
		}
		return err
	}
	return nil
}
