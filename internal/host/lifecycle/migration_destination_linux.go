//go:build linux

package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ValidateMigrationBackupDestination checks the operator's private ciphertext
// destination before admission, source freezing or provider preparation.
func ValidateMigrationBackupDestination(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return fmt.Errorf("choose a private absolute operator backup path")
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(path), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return fmt.Errorf("create the private operator backup directory before preparing migration")
	}
	defer unix.Close(fd)
	var info unix.Stat_t
	if unix.Fstat(fd, &info) != nil || info.Uid != uint32(os.Geteuid()) || info.Mode&07777 != 0700 {
		return fmt.Errorf("operator backup directory must be private and owned")
	}
	if err := unix.Fstatat(fd, filepath.Base(path), &info, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("choose an unused operator backup filename")
	}
	return nil
}
