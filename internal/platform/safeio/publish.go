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
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".loki-private-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)

	if err = f.Chmod(0600); err == nil {
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
