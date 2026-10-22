//go:build linux

package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// VerifyMigrationSourceArchive reconfirms a prepared or published ciphertext receipt.
// Neither source plaintext nor an age identity is needed for this check.
func VerifyMigrationSourceArchive(path string, archive *MigrationArchiveCheckpoint) error {
	if archive == nil || archive.Bytes < 1 || archive.Bytes > 129<<20 || !migrationHex(archive.CipherSHA256, 32) {
		return fmt.Errorf("original encrypted source backup metadata is unavailable")
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return fmt.Errorf("original encrypted source backup is unavailable")
	}
	file := os.NewFile(uintptr(fd), "original-encrypted-source-backup")
	defer file.Close()
	var info unix.Stat_t
	if unix.Fstat(fd, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&07777 != 0600 || info.Uid != uint32(os.Geteuid()) || info.Nlink != 1 || info.Size != archive.Bytes {
		return fmt.Errorf("original encrypted source backup ownership or size changed")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(file, archive.Bytes+1)); err != nil || hex.EncodeToString(digest.Sum(nil)) != archive.CipherSHA256 {
		return fmt.Errorf("original encrypted source backup integrity changed")
	}
	return nil
}
