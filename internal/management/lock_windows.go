package management

import (
	"loki/internal/platform/filelock"
	"os"
)

func isLockBusy(err error) bool { return filelock.Busy(err) }

func lockFile(f *os.File) (func(), error) {
	return filelock.Exclusive(f)
}

func sharedLockFile(f *os.File) (func(), error) { return filelock.Shared(f) }
