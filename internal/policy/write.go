package policy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Parent pins a real parent directory, creating missing components if requested.
func (w *Workspace) Parent(requested string, create bool) (*os.File, string, error) {
	relative, err := Relative(requested)
	if err != nil {
		return nil, "", err
	}
	if relative == "." {
		return nil, "", unix.EISDIR
	}
	fd, err := unix.Dup(int(w.directory.Fd()))
	if err != nil {
		return nil, "", err
	}
	current := os.NewFile(uintptr(fd), "workspace")
	for _, part := range strings.Split(filepath.Dir(relative), "/") {
		if part == "." {
			continue
		}
		next, err := unix.Openat2(int(current.Fd()), part, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if errors.Is(err, unix.ENOENT) && create {
			if err = unix.Mkdirat(int(current.Fd()), part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
				current.Close()
				return nil, "", err
			}
			next, err = unix.Openat2(int(current.Fd()), part, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		}
		current.Close()
		if err != nil {
			return nil, "", err
		}
		current = os.NewFile(uintptr(next), part)
	}
	return current, filepath.Base(relative), nil
}

// AtomicWrite publishes through a pinned directory without following links.
func (w *Workspace) AtomicWrite(requested string, data []byte, mode fs.FileMode, overwrite bool) error {
	parent, name, err := w.Parent(requested, true)
	if err != nil {
		return err
	}
	defer parent.Close()
	suffix := make([]byte, 12)
	if _, err = rand.Read(suffix); err != nil {
		return err
	}
	temporary := ".loki-write-" + hex.EncodeToString(suffix)
	fd, err := unix.Openat(int(parent.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(parent.Fd()), temporary, 0)
	f := os.NewFile(uintptr(fd), temporary)
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode.Perm())
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	flags := uint(0)
	if !overwrite {
		flags = unix.RENAME_NOREPLACE
	}
	if err = unix.Renameat2(int(parent.Fd()), temporary, int(parent.Fd()), name, flags); err != nil {
		return err
	}
	return parent.Sync()
}

func (w *Workspace) Move(source, destination string) error {
	src, srcName, err := w.Parent(source, false)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, dstName, err := w.Parent(destination, true)
	if err != nil {
		return err
	}
	defer dst.Close()
	if err = unix.Renameat2(int(src.Fd()), srcName, int(dst.Fd()), dstName, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	if err = src.Sync(); err != nil {
		return err
	}
	return dst.Sync()
}

func (w *Workspace) RemoveFile(path string) error {
	parent, name, err := w.Parent(path, false)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err = unix.Unlinkat(int(parent.Fd()), name, 0); err != nil {
		return err
	}
	return parent.Sync()
}
