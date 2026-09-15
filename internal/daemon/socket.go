package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func PrivateDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("service directory must be absolute")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Clean(path) {
		return errors.New("service directory must not contain symlinks")
	}
	var stat unix.Stat_t
	if err = unix.Stat(path, &stat); err != nil || stat.Uid != uint32(os.Getuid()) || stat.Mode&0022 != 0 {
		return errors.New("service directory must be service-owned and not writable by other users")
	}
	return nil
}

func OwnedPrivateDirectory(path string, uid, gid uint32) error {
	if !filepath.IsAbs(path) {
		return errors.New("owned directory must be absolute")
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC | unix.O_DIRECTORY,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if errors.Is(err, unix.ENOSYS) {
		fd, err = openNoSymlinks(path, unix.O_PATH|unix.O_DIRECTORY)
	}
	if err != nil {
		return fmt.Errorf("cannot open owned directory without symlinks: %w", err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Uid != uid || stat.Gid != gid || stat.Mode&07777 != 0700 {
		return errors.New("owned directory has unsafe ownership or permissions")
	}
	return nil
}

func Listen(socket string, gid int) (*net.UnixListener, error) {
	if !filepath.IsAbs(socket) || gid < 0 {
		return nil, errors.New("invalid service socket layout")
	}
	parent := filepath.Dir(socket)
	_, statErr := os.Lstat(parent)
	created := errors.Is(statErr, os.ErrNotExist)
	if err := PrivateDirectory(parent); err != nil {
		return nil, err
	}
	if created {
		if err := os.Chown(parent, -1, gid); err != nil {
			return nil, err
		}
		if err := os.Chmod(parent, 0750); err != nil {
			return nil, err
		}
	} else {
		var stat unix.Stat_t
		if err := unix.Stat(parent, &stat); err != nil || stat.Gid != uint32(gid) || stat.Mode&0050 != 0050 {
			return nil, errors.New("existing socket directory must already permit its configured group")
		}
	}
	if err := removeStaleSocket(socket); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err = os.Chown(socket, -1, gid); err == nil {
		err = os.Chmod(socket, 0660)
	}
	if err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var stat unix.Stat_t
	if err = unix.Lstat(path, &stat); err != nil || info.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Getuid()) {
		return errors.New("existing service socket is not an owned Unix socket")
	}
	connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		connection.Close()
		return errors.New("service socket is already active")
	}
	if !errors.Is(dialErr, unix.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
		return fmt.Errorf("cannot determine service socket state: %w", dialErr)
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
