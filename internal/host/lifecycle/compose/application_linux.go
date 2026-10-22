//go:build linux

package compose

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// A private host-operator ancestor prevents fixed container UID10006 from
// accessing another deployment through the host's global /dev/shm namespace.
func applicationRuntimeDirectory(operatorRoot, parent string) (string, error) {
	var operator, st unix.Stat_t
	if unix.Lstat(operatorRoot, &operator) != nil {
		return "", errors.New("host operator identity is unavailable")
	}
	if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	var fs unix.Statfs_t
	if unix.Lstat(parent, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || st.Uid != operator.Uid || unix.Statfs(parent, &fs) != nil || fs.Type != unix.TMPFS_MAGIC {
		return "", errors.New("application runtime ancestor is not private host-owned tmpfs")
	}
	path := filepath.Join(parent, "secrets")
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if unix.Lstat(path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != 0 && st.Uid != operator.Uid {
		return "", errors.New("application runtime root requires recovery")
	}
	return path, nil
}
