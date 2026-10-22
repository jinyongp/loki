//go:build linux

package application

import (
	"errors"
	"io"
	"os"
	"regexp"

	"golang.org/x/sys/unix"
)

var inboxEntry = regexp.MustCompile(`^([0-9a-f]{32}\.(data|json)|\.loki-private-[0-9]{1,20})$`)

// ResetInbox expires pending private input when a new broker instance starts.
// The caller must hold the exclusive broker runtime lease. Only known owned
// stage files in the private tmpfs inbox are removed; durable attribution and
// provider receipts live outside this directory and remain replayable.
func ResetInbox(path string) error {
	if privateDirectory(path, true) != nil {
		return unavailable()
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return unavailable()
	}
	directory := os.NewFile(uintptr(fd), "private-stage-inbox")
	defer directory.Close()
	entries, err := directory.ReadDir(4097)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 4096 {
		return unavailable()
	}
	for _, entry := range entries {
		var st unix.Stat_t
		if !inboxEntry.MatchString(entry.Name()) || unix.Fstatat(fd, entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
			return unavailable()
		}
	}
	for _, entry := range entries {
		if unix.Unlinkat(fd, entry.Name(), 0) != nil {
			return unavailable()
		}
	}
	return directory.Sync()
}
