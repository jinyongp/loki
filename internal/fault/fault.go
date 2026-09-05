// Package fault distinguishes safe operational messages from private failures.
package fault

import (
	"context"
	"errors"
	"io/fs"
	"syscall"
)

type Error string

func (e Error) Error() string { return string(e) }

func Public(err error) string {
	var safe Error
	if errors.As(err, &safe) {
		return safe.Error()
	}
	for _, item := range []struct {
		err     error
		message string
	}{
		{fs.ErrNotExist, "requested path or required executable was not found; verify the path and installed tool"},
		{fs.ErrExist, "destination already exists; choose another path or explicitly allow overwrite"},
		{fs.ErrPermission, "workspace permissions denied the operation; verify ownership and writable scope"},
		{syscall.ENOTDIR, "a directory was required at the requested path"},
		{syscall.EISDIR, "a regular file was required at the requested path"},
		{context.DeadlineExceeded, "operation timed out; narrow the request or increase its timeout"},
		{syscall.ENOSPC, "workspace storage is full; free space and retry"},
		{syscall.EROFS, "the target is read-only; write inside the workspace"},
		{syscall.EBUSY, "the requested resource is busy; stop the using process and retry"},
		{syscall.ENAMETOOLONG, "the requested path or filename is too long"},
		{syscall.EMFILE, "the process has too many open files; close sessions and retry"},
	} {
		if errors.Is(err, item.err) {
			return item.message
		}
	}
	return "unexpected server failure; run diagnostics and retry"
}
