//go:build linux

// Package repository resolves the owning Git metadata without executing project
// configuration, hooks or binaries. Its identity matches Git ContextEvidence.
package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var unavailable = errors.New("repository identity unavailable")

type Resolver struct {
	root      string
	directory *os.File
}

func New(root string) (*Resolver, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return nil, unavailable
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, unavailable
	}
	return &Resolver{root: root, directory: os.NewFile(uintptr(fd), "repository-workspace")}, nil
}

func (r *Resolver) Close() error { return r.directory.Close() }

func (r *Resolver) open(path string) (*os.File, error) {
	if !filepath.IsLocal(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return nil, unavailable
	}
	fd, err := unix.Openat2(int(r.directory.Fd()), path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "repository-metadata")
	info, err := f.Stat()
	if err != nil || !info.IsDir() && !info.Mode().IsRegular() {
		f.Close()
		return nil, unavailable
	}
	return f, nil
}

func (r *Resolver) read(path string) (string, error) {
	f, err := r.open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", unavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", unavailable
	}
	return string(data), nil
}

func (r *Resolver) directoryExists(path string) bool {
	f, err := r.open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	return err == nil && info.IsDir()
}

func (r *Resolver) metadataPath(base, value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return "", unavailable
	}
	path := filepath.Clean(filepath.Join(base, value))
	if filepath.IsAbs(value) {
		var err error
		path, err = filepath.Rel(r.root, value)
		if err != nil {
			return "", unavailable
		}
	}
	if !filepath.IsLocal(path) {
		return "", unavailable
	}
	return path, nil
}

func (r *Resolver) Identity(cwd string) (string, error) {
	if !filepath.IsLocal(cwd) || filepath.Clean(cwd) != cwd || !r.directoryExists(cwd) {
		return "", unavailable
	}
	for current := cwd; ; current = filepath.Dir(current) {
		metadata := filepath.Join(current, ".git")
		file, err := r.open(metadata)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", unavailable
			}
			if current == "." {
				return "", unavailable
			}
			continue
		}
		info, err := file.Stat()
		file.Close()
		if err != nil {
			return "", unavailable
		}
		gitdir := metadata
		if !info.IsDir() {
			body, err := r.read(metadata)
			if err != nil {
				return "", unavailable
			}
			body = strings.TrimSuffix(body, "\n")
			value, ok := strings.CutPrefix(body, "gitdir: ")
			if !ok {
				return "", unavailable
			}
			gitdir, err = r.metadataPath(current, value)
			if err != nil {
				return "", unavailable
			}
		}
		if _, err = r.read(filepath.Join(gitdir, "HEAD")); err != nil {
			return "", unavailable
		}
		common := gitdir
		if body, err := r.read(filepath.Join(gitdir, "commondir")); err == nil {
			common, err = r.metadataPath(gitdir, strings.TrimSuffix(body, "\n"))
			if err != nil {
				return "", unavailable
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", unavailable
		}
		if !r.directoryExists(filepath.Join(common, "objects")) || !r.directoryExists(filepath.Join(common, "refs")) {
			return "", unavailable
		}
		if _, err = r.read(filepath.Join(common, "config")); err != nil {
			return "", unavailable
		}
		sum := sha256.Sum256([]byte("loki-git-common-v1\x00" + filepath.ToSlash(common)))
		return hex.EncodeToString(sum[:]), nil
	}
}
