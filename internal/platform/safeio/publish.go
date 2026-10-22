// Package safeio provides reusable low-level filesystem publication mechanics.
package safeio

import (
	"os"
	"path/filepath"
)

// PublishPrivate atomically publishes data as a private 0600 regular file in
// the destination's existing parent directory. The parent path is trusted input;
// untrusted workspace paths require the stronger pinned workspace writer.
func PublishPrivate(path string, data []byte, overwrite bool) error {
	return publishPrivate(path, data, overwrite, nil)
}

// PublishPrivateOwned sets the trusted destination owner's identity before
// publication. A crash must not expose a root-owned replacement that its
// ordinary service owner cannot read or enforce after restart.
func PublishPrivateOwned(path string, data []byte, overwrite bool, uid, gid int) error {
	return publishPrivate(path, data, overwrite, func(f *os.File) error {
		return f.Chown(uid, gid)
	})
}

func publishPrivate(path string, data []byte, overwrite bool, prepare func(*os.File) error) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".loki-private-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)

	if err = f.Chmod(0600); err == nil {
		if prepare != nil {
			err = prepare(f)
		}
	}
	if err == nil {
		_, err = f.Write(data)
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

	if overwrite {
		err = os.Rename(name, path)
	} else {
		err = os.Link(name, path)
	}
	if err != nil {
		return err
	}

	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
