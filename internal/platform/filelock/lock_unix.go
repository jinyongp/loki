//go:build linux || darwin

// Package filelock provides nonblocking kernel leases on persistent files.
package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func Busy(err error) bool                     { return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) }
func Exclusive(file *os.File) (func(), error) { return acquire(file, unix.LOCK_EX) }
func Shared(file *os.File) (func(), error)    { return acquire(file, unix.LOCK_SH) }
func acquire(file *os.File, mode int) (func(), error) {
	if err := unix.Flock(int(file.Fd()), mode|unix.LOCK_NB); err != nil {
		return nil, err
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }, nil
}
