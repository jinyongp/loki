package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"loki/internal/host/lifecycle"
)

type hostCLIInstallPaths struct {
	ReleaseRoot string
	Binary      string
	Link        string
}

func resolveHostCLIInstallPaths(system bool, generationID string) (hostCLIInstallPaths, error) {
	if !strings.HasPrefix(generationID, "sha256:") || len(generationID) != len("sha256:")+64 {
		return hostCLIInstallPaths{}, errors.New("host CLI generation identity is invalid")
	}
	id := strings.TrimPrefix(generationID, "sha256:")
	if _, err := hex.DecodeString(id); err != nil {
		return hostCLIInstallPaths{}, errors.New("host CLI generation identity is invalid")
	}
	var releaseRoot, link string
	if system {
		releaseRoot = "/opt/loki-host/releases"
		link = "/usr/local/bin/loki"
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return hostCLIInstallPaths{}, err
		}
		if !filepath.IsAbs(home) || filepath.Clean(home) != home || home == string(filepath.Separator) {
			return hostCLIInstallPaths{}, errors.New("user home directory is invalid")
		}
		releaseRoot = filepath.Join(home, ".local", "lib", "loki", "releases")
		link = filepath.Join(home, ".local", "bin", "loki")
	}
	return hostCLIInstallPaths{
		ReleaseRoot: releaseRoot,
		Binary:      filepath.Join(releaseRoot, id, "loki"),
		Link:        link,
	}, nil
}

func preflightHostCLIInstall(paths hostCLIInstallPaths) error {
	info, err := os.Lstat(paths.Link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s is occupied by an unmanaged file", paths.Link)
	}
	target, err := os.Readlink(paths.Link)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(paths.Link), target)
	}
	target = filepath.Clean(target)
	if !pathContains(filepath.Clean(paths.ReleaseRoot), target) {
		return fmt.Errorf("%s is occupied by an unmanaged symlink", paths.Link)
	}
	return nil
}

func publishHostCLI(paths hostCLIInstallPaths, generation lifecycle.Generation) error {
	if !generation.Valid() {
		return errors.New("host CLI release generation is invalid")
	}
	if err := preflightHostCLIInstall(paths); err != nil {
		return err
	}
	if err := ensureHostCLIDirectory(paths.ReleaseRoot, 0755); err != nil {
		return err
	}
	if err := ensureHostCLIDirectory(filepath.Dir(paths.Link), 0755); err != nil {
		return err
	}
	versionDir := filepath.Dir(paths.Binary)
	if err := ensureHostCLIDirectory(versionDir, 0755); err != nil {
		return err
	}
	if err := publishCurrentExecutable(paths.Binary, generation.Spec.HostBinaryDigest); err != nil {
		return err
	}
	return publishManagedCLILink(paths)
}

func stageHostCLI(paths hostCLIInstallPaths, generation lifecycle.Generation, binary []byte) error {
	if !generation.Valid() {
		return errors.New("host CLI release generation is invalid")
	}
	if err := preflightHostCLIInstall(paths); err != nil {
		return err
	}
	if err := ensureHostCLIDirectory(paths.ReleaseRoot, 0755); err != nil {
		return err
	}
	versionDir := filepath.Dir(paths.Binary)
	if err := ensureHostCLIDirectory(versionDir, 0755); err != nil {
		return err
	}
	return publishVerifiedExecutable(paths.Binary, binary, generation.Spec.HostBinaryDigest)
}

func ensureHostCLIDirectory(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("host CLI installation directory must be a real directory")
	}
	return os.Chmod(path, mode)
}

func publishCurrentExecutable(destination, expectedDigest string) error {
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0111 == 0 {
			return errors.New("managed host CLI binary path contains an invalid object")
		}
		return verifyFileDigest(destination, expectedDigest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	source, err := os.Executable()
	if err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	temp, err := os.CreateTemp(filepath.Dir(destination), ".loki-host-binary-")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err = temp.Chmod(0755); err != nil {
		temp.Close()
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(temp, hash), input)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if got != expectedDigest {
		return errors.New("running host binary no longer matches the verified release")
	}
	if err = unix.Renameat2(unix.AT_FDCWD, tempName, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return verifyFileDigest(destination, expectedDigest)
		}
		return err
	}
	return syncHostCLIDirectory(filepath.Dir(destination))
}

func publishVerifiedExecutable(destination string, raw []byte, expectedDigest string) error {
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0111 == 0 {
			return errors.New("managed host CLI binary path contains an invalid object")
		}
		return verifyFileDigest(destination, expectedDigest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sum := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(sum[:]) != expectedDigest {
		return errors.New("verified host CLI binary digest does not match the release")
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".loki-host-binary-")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err = temp.Chmod(0755); err != nil {
		temp.Close()
		return err
	}
	if _, err = temp.Write(raw); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = unix.Renameat2(unix.AT_FDCWD, tempName, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return verifyFileDigest(destination, expectedDigest)
		}
		return err
	}
	return syncHostCLIDirectory(filepath.Dir(destination))
}

func verifyFileDigest(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != expected {
		return errors.New("managed host CLI binary digest does not match the release")
	}
	return nil
}

func publishManagedCLILink(paths hostCLIInstallPaths) error {
	parent := filepath.Dir(paths.Link)
	tempDir, err := os.MkdirTemp(parent, ".loki-host-link-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	tempLink := filepath.Join(tempDir, "loki")
	if err = os.Symlink(paths.Binary, tempLink); err != nil {
		return err
	}

	_, statErr := os.Lstat(paths.Link)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		err = unix.Renameat2(unix.AT_FDCWD, tempLink, unix.AT_FDCWD, paths.Link, unix.RENAME_NOREPLACE)
	case statErr != nil:
		return statErr
	default:
		if err = preflightHostCLIInstall(paths); err != nil {
			return err
		}
		err = os.Rename(tempLink, paths.Link)
	}
	if err != nil {
		return err
	}
	return syncHostCLIDirectory(parent)
}

func syncHostCLIDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
