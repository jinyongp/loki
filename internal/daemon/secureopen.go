package daemon

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openNoSymlinks opens an absolute path while rejecting symlinks in every
// component. The openat walk preserves the same property on services whose
// systemd sandbox deliberately makes openat2 unavailable.
func openNoSymlinks(path string, flags int) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return -1, errors.New("path must be a clean absolute non-root path")
	}
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, part := range parts {
		componentFlags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index == len(parts)-1 {
			componentFlags = flags | unix.O_CLOEXEC | unix.O_NOFOLLOW
		}
		next, openErr := unix.Openat(fd, part, componentFlags, 0)
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}
