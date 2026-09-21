package toolchain

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const generationMetadataVersion = 1

type GenerationStore struct {
	Root string
}

type Generation struct {
	ID         string
	Path       string
	Root       string
	TreeSHA256 string
}

type generationMetadata struct {
	Version    int    `json:"version"`
	ID         string `json:"id"`
	TreeSHA256 string `json:"tree_sha256"`
}

type GenerationLease struct {
	file *os.File
	path string
}

func (s GenerationStore) validate() error {
	if !filepath.IsAbs(s.Root) || filepath.Clean(s.Root) != s.Root || s.Root == string(filepath.Separator) {
		return errors.New("toolchain generation store root must be a clean absolute non-root path")
	}
	return nil
}

func (s GenerationStore) prepare() error {
	if err := s.validate(); err != nil {
		return err
	}
	for path, mode := range map[string]os.FileMode{
		s.Root:                               0755,
		filepath.Join(s.Root, "generations"): 0755,
		filepath.Join(s.Root, "staging"):     0700,
		filepath.Join(s.Root, "locks"):       0700,
		filepath.Join(s.Root, "refs"):        0700,
	} {
		if err := os.MkdirAll(path, mode); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("toolchain generation store path %s is not a directory", path)
		}
	}
	return nil
}

func validateGenerationID(id string) error {
	if !sha256Text.MatchString(id) {
		return errors.New("toolchain generation id must be a lowercase sha256 digest")
	}
	return nil
}

func (s GenerationStore) generationPath(id string) string {
	return filepath.Join(s.Root, "generations", id)
}

func (s GenerationStore) Lookup(id string) (Generation, error) {
	if err := s.prepare(); err != nil {
		return Generation{}, err
	}
	if err := validateGenerationID(id); err != nil {
		return Generation{}, err
	}
	path := s.generationPath(id)
	raw, err := os.ReadFile(filepath.Join(path, "generation.json"))
	if err != nil {
		return Generation{}, err
	}
	var metadata generationMetadata
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&metadata); err != nil {
		return Generation{}, errors.New("toolchain generation metadata is invalid")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Generation{}, errors.New("toolchain generation metadata contains trailing data")
	}
	if metadata.Version != generationMetadataVersion || metadata.ID != id || !sha256Text.MatchString(metadata.TreeSHA256) {
		return Generation{}, errors.New("toolchain generation metadata does not match its identity")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode().Perm()&0222 != 0 {
		return Generation{}, errors.New("toolchain generation directory is missing or mutable")
	}
	root := filepath.Join(path, "root")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0222 != 0 {
		return Generation{}, errors.New("toolchain generation root is missing or mutable")
	}
	return Generation{ID: id, Path: path, Root: root, TreeSHA256: metadata.TreeSHA256}, nil
}

func (s GenerationStore) Provision(ctx context.Context, id string, install func(context.Context, string) error) (Generation, error) {
	if install == nil {
		return Generation{}, errors.New("toolchain generation installer is required")
	}
	if err := s.prepare(); err != nil {
		return Generation{}, err
	}
	if err := validateGenerationID(id); err != nil {
		return Generation{}, err
	}
	release, err := s.lock(ctx, "generation-"+id)
	if err != nil {
		return Generation{}, err
	}
	defer release()

	if generation, lookupErr := s.Lookup(id); lookupErr == nil {
		return generation, nil
	} else if !errors.Is(lookupErr, os.ErrNotExist) {
		return Generation{}, lookupErr
	}

	staging, err := os.MkdirTemp(filepath.Join(s.Root, "staging"), "."+id+"-")
	if err != nil {
		return Generation{}, err
	}
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = makeTreeWritable(staging)
			_ = os.RemoveAll(staging)
		}
	}()
	root := filepath.Join(staging, "root")
	if err = os.Mkdir(root, 0755); err != nil {
		return Generation{}, err
	}
	if err = install(ctx, root); err != nil {
		return Generation{}, err
	}
	if err = freezeTree(root); err != nil {
		return Generation{}, err
	}
	treeSHA, err := treeDigest(root)
	if err != nil {
		return Generation{}, err
	}
	metadata := generationMetadata{Version: generationMetadataVersion, ID: id, TreeSHA256: treeSHA}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return Generation{}, err
	}
	if err = os.WriteFile(filepath.Join(staging, "generation.json"), append(encoded, '\n'), 0444); err != nil {
		return Generation{}, err
	}
	target := s.generationPath(id)
	if err = os.Rename(staging, target); err != nil {
		return Generation{}, err
	}
	removeStaging = false
	if err = os.Chmod(target, 0555); err != nil {
		_ = makeTreeWritable(target)
		_ = os.RemoveAll(target)
		return Generation{}, err
	}
	if err = syncDirectory(filepath.Join(s.Root, "generations")); err != nil {
		return Generation{}, err
	}
	return s.Lookup(id)
}

func (s GenerationStore) LockArtifact(ctx context.Context, digest string) (func(), error) {
	if !sha256Text.MatchString(digest) {
		return nil, errors.New("toolchain artifact lock requires a lowercase sha256 digest")
	}
	if err := s.prepare(); err != nil {
		return nil, err
	}
	return s.lock(ctx, "artifact-"+digest)
}

func (s GenerationStore) lock(ctx context.Context, name string) (func(), error) {
	path := filepath.Join(s.Root, "locks", name+".lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name+".lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		_ = file.Close()
		return nil, errors.New("toolchain lock must be a private regular file")
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (s GenerationStore) Acquire(id string) (*GenerationLease, error) {
	if _, err := s.Lookup(id); err != nil {
		return nil, err
	}
	refDir := filepath.Join(s.Root, "refs", id)
	if err := os.MkdirAll(refDir, 0700); err != nil {
		return nil, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	path := filepath.Join(refDir, hex.EncodeToString(random)+".ref")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return &GenerationLease{file: file, path: path}, nil
}

func (l *GenerationLease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	err = errors.Join(err, l.file.Close())
	err = errors.Join(err, os.Remove(l.path))
	l.file = nil
	l.path = ""
	return err
}

func (s GenerationStore) InUse(id string) (bool, error) {
	if err := s.prepare(); err != nil {
		return false, err
	}
	if err := validateGenerationID(id); err != nil {
		return false, err
	}
	refDir := filepath.Join(s.Root, "refs", id)
	entries, err := os.ReadDir(refDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ref") {
			return false, errors.New("toolchain generation reference directory contains an invalid entry")
		}
		path := filepath.Join(refDir, entry.Name())
		fd, openErr := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return false, openErr
		}
		lockErr := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(lockErr, unix.EWOULDBLOCK) {
			_ = unix.Close(fd)
			return true, nil
		}
		if lockErr != nil {
			_ = unix.Close(fd)
			return false, lockErr
		}
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	_ = os.Remove(refDir)
	return false, nil
}

func freezeTree(root string) error {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		paths = append(paths, path)
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir(), info.Mode().IsRegular():
			return nil
		default:
			return fmt.Errorf("toolchain generation contains unsupported object %q", path)
		}
	})
	if err != nil {
		return err
	}
	sort.Slice(paths, func(i, j int) bool {
		return strings.Count(paths[i], string(filepath.Separator)) > strings.Count(paths[j], string(filepath.Separator))
	})
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			if err != nil {
				return err
			}
			continue
		}
		mode := os.FileMode(0444)
		if info.IsDir() || info.Mode().Perm()&0111 != 0 {
			mode = 0555
		}
		if err = os.Chmod(path, mode); err != nil {
			return err
		}
	}
	return nil
}

func makeTreeWritable(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm() | 0200
		if info.IsDir() {
			mode |= 0700
		}
		return os.Chmod(path, mode)
	})
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
