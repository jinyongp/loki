package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ReadJSON accepts only bounded administrator/service-owned layout files.
// A writable or symlinked layout could redefine privileged execution paths.
func ReadJSON(path string, out any) error {
	if !filepath.IsAbs(path) {
		return errors.New("service layout path must be absolute")
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if errors.Is(err, unix.ENOSYS) {
		fd, err = openNoSymlinks(path, unix.O_RDONLY|unix.O_NONBLOCK)
	}
	if err != nil {
		return fmt.Errorf("cannot open trusted service layout: %w", err)
	}
	f := os.NewFile(uintptr(fd), "service-layout")
	defer f.Close()
	var info unix.Stat_t
	if err = unix.Fstat(fd, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&0022 != 0 || (info.Uid != 0 && info.Uid != uint32(os.Getuid())) {
		return errors.New("service layout must be a protected administrator-owned regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1048577))
	if err != nil || len(data) > 1048576 {
		return errors.New("service layout exceeds limit or cannot be read")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("service layout must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return errors.New("invalid service layout")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("invalid trailing service layout")
	}
	return nil
}
