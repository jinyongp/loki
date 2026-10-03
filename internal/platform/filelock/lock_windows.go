package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func Busy(err error) bool                     { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
func Exclusive(file *os.File) (func(), error) { return acquire(file, windows.LOCKFILE_EXCLUSIVE_LOCK) }
func Shared(file *os.File) (func(), error)    { return acquire(file, 0) }
func acquire(file *os.File, mode uint32) (func(), error) {
	var overlap windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(file.Fd()), mode|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap); err != nil {
		return nil, err
	}
	return func() { _ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlap) }, nil
}
