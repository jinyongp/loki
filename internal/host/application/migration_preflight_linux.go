//go:build linux

package application

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var errMaterializedGeneration = errors.New("existing application generation contains state; preserve it through application backup/restore before legacy migration")

// RequireExistingGeneration prevents restart/selection from manufacturing an
// empty replacement for an already prepared generation. Only directory
// identities are inspected; provider files and keys remain opaque.
func RequireExistingGeneration(stateRoot string, identity Identity) error {
	if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot || stateRoot == "/" || !generationID.MatchString(identity.ActiveGeneration) || identity.UID == 0 || identity.GID == 0 {
		return invalid
	}
	root := filepath.Join(stateRoot, "generations", identity.ActiveGeneration)
	for _, path := range []string{root, filepath.Join(root, "dvt"), filepath.Join(root, "metadata")} {
		fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
		if err != nil {
			return invalid
		}
		var info unix.Stat_t
		err = unix.Fstat(fd, &info)
		unix.Close(fd)
		if err != nil || info.Uid != identity.UID || info.Gid != identity.GID || info.Mode&07777 != 0700 {
			return invalid
		}
	}
	return nil
}

// RequireEmptyGeneration protects already managed application values and
// mutation history from being silently dropped by a legacy-source migration.
// It examines directory metadata only, without opening provider data or keys.
// The host repeats this after stopping writers and before selector publication.
func RequireEmptyGeneration(stateRoot string, identity Identity) error {
	if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot || stateRoot == "/" || !generationID.MatchString(identity.ActiveGeneration) || identity.UID == 0 || identity.GID == 0 {
		return invalid
	}
	root := filepath.Join(stateRoot, "generations", identity.ActiveGeneration)
	open := func(path string) (*os.File, error) {
		fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
		if err != nil {
			return nil, invalid
		}
		var info unix.Stat_t
		if unix.Fstat(fd, &info) != nil || info.Uid != identity.UID || info.Gid != identity.GID || info.Mode&07777 != 0700 {
			unix.Close(fd)
			return nil, invalid
		}
		return os.NewFile(uintptr(fd), "original-generation-directory"), nil
	}
	data, err := open(filepath.Join(root, "dvt"))
	if err != nil {
		return err
	}
	entries, readErr := data.ReadDir(1)
	data.Close()
	if len(entries) != 0 || readErr != io.EOF {
		return errMaterializedGeneration
	}
	metadata, err := open(filepath.Join(root, "metadata"))
	if err != nil {
		return err
	}
	defer metadata.Close()
	entries, err = metadata.ReadDir(3)
	if err != nil && err != io.EOF || len(entries) > 2 {
		return errMaterializedGeneration
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "requests":
			requests, err := open(filepath.Join(root, "metadata", "requests"))
			if err != nil {
				return err
			}
			items, readErr := requests.ReadDir(1)
			requests.Close()
			if len(items) != 0 || readErr != io.EOF {
				return errMaterializedGeneration
			}
		case "maintenance.json":
			// A host gate contains no app data or mutation replay identity.
			var info unix.Stat_t
			if unix.Fstatat(int(metadata.Fd()), entry.Name(), &info, unix.AT_SYMLINK_NOFOLLOW) != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&07777 != 0600 || info.Uid != identity.UID || info.Gid != identity.GID || info.Nlink != 1 {
				return invalid
			}
		default:
			return errMaterializedGeneration
		}
	}
	return nil
}
