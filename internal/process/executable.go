package process

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// executablePath follows the child's PATH, not the server's ambient PATH.
// exec.Command normally performs lookup before cmd.Env is assigned.
func executablePath(name, cwd string, environment []string) (string, error) {
	if strings.ContainsRune(name, '/') {
		return name, nil
	}
	searchPath := "/bin:/usr/bin" // POSIX default when PATH is absent.
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			searchPath = value
		}
	}
	var permission error
	for _, directory := range strings.Split(searchPath, ":") {
		candidate := filepath.Join(directory, name)
		if !filepath.IsAbs(candidate) {
			var err error
			candidate, err = filepath.Abs(filepath.Join(cwd, candidate))
			if err != nil {
				return "", err
			}
		}
		info, err := os.Stat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				permission = os.ErrPermission
			}
			continue
		}
		if !info.Mode().IsRegular() {
			permission = os.ErrPermission
			continue
		}
		if err = unix.Faccessat(unix.AT_FDCWD, candidate, unix.X_OK, unix.AT_EACCESS); err == nil {
			return candidate, nil
		}
		if errors.Is(err, unix.EACCES) {
			permission = os.ErrPermission
		}
	}
	if permission != nil {
		return "", &exec.Error{Name: name, Err: permission}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}
