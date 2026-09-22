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
	Root      string
	Limits    GenerationLimits
	Protected []string
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
	if err := s.validate(); err != nil {
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
	limits, err := s.normalizedLimits()
	if err != nil {
		return Generation{}, err
	}
	stagedBytes, err := generationDiskUsage(staging)
	if err != nil {
		return Generation{}, err
	}
	releaseStorage, err := s.lock(ctx, "storage")
	if err != nil {
		return Generation{}, err
	}
	defer releaseStorage()
	if _, err = s.collectLocked(ctx, time.Now().UTC(), limits, stagedBytes, 1); err != nil {
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

func (s GenerationStore) tryLock(name string) (func(), bool, error) {
	if err := s.prepare(); err != nil {
		return nil, false, err
	}
	path := filepath.Join(s.Root, "locks", name+".lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(fd), name+".lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		_ = file.Close()
		return nil, false, errors.New("toolchain lock must be a private regular file")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = file.Close()
	}, true, nil
}

func (s GenerationStore) lock(ctx context.Context, name string) (func(), error) {
	for {
		release, locked, err := s.tryLock(name)
		if err != nil {
			return nil, err
		}
		if locked {
			return release, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func validateReferenceOwner(owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" || len(owner) > 128 || strings.ContainsAny(owner, "/\\\r\n\x00") {
		return errors.New("toolchain generation reference owner is invalid")
	}
	for _, character := range owner {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("._:-", character) {
			continue
		}
		return errors.New("toolchain generation reference owner is invalid")
	}
	return nil
}

func referenceToken() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}

func validReferenceName(name, suffix string) bool {
	if len(name) != 32+len(suffix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimSuffix(name, suffix))
	return err == nil
}

func readReferenceOwner(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 2 || info.Size() > 129 {
		return "", errors.New("toolchain generation reference is invalid")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return "", errors.New("toolchain generation reference is invalid")
	}
	owner := strings.TrimSuffix(string(raw), "\n")
	if err = validateReferenceOwner(owner); err != nil {
		return "", err
	}
	return owner, nil
}

func (s GenerationStore) Acquire(id, owner string) (*GenerationLease, error) {
	return s.AcquireContext(context.Background(), id, owner)
}

func (s GenerationStore) AcquireContext(ctx context.Context, id, owner string) (*GenerationLease, error) {
	releaseGeneration, err := s.lock(ctx, "generation-"+id)
	if err != nil {
		return nil, err
	}
	defer releaseGeneration()
	if _, err = s.Lookup(id); err != nil {
		return nil, err
	}
	owner = strings.TrimSpace(owner)
	if err = validateReferenceOwner(owner); err != nil {
		return nil, err
	}
	refDir := filepath.Join(s.Root, "refs", id)
	if err := os.MkdirAll(refDir, 0700); err != nil {
		return nil, err
	}
	token, err := referenceToken()
	if err != nil {
		return nil, err
	}
	temporary := filepath.Join(refDir, token+".tmp")
	path := filepath.Join(refDir, token+".ref")
	fd, err := unix.Open(temporary, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	temporaryFile := os.NewFile(uintptr(fd), filepath.Base(temporary))
	cleanupTemporary := true
	defer func() {
		_ = temporaryFile.Close()
		if cleanupTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if _, err = temporaryFile.Write([]byte(owner + "\n")); err != nil {
		return nil, err
	}
	if err = temporaryFile.Sync(); err != nil {
		return nil, err
	}
	if err = temporaryFile.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(temporary, path); err != nil {
		return nil, err
	}
	cleanupTemporary = false
	if err = syncDirectory(refDir); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	fd, err = unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	leaseFile := os.NewFile(uintptr(fd), filepath.Base(path))
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = leaseFile.Close()
		return nil, err
	}
	return &GenerationLease{file: leaseFile, path: path}, nil
}

func (l *GenerationLease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	err = errors.Join(err, l.file.Close())
	l.file = nil
	return err
}

func (l *GenerationLease) Release() error {
	if l == nil {
		return nil
	}
	err := l.Close()
	if l.path != "" {
		removeErr := os.Remove(l.path)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		err = errors.Join(err, removeErr)
		l.path = ""
	}
	return err
}

func (s GenerationStore) ReleaseOwner(id, owner string) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := validateGenerationID(id); err != nil {
		return err
	}
	releaseGeneration, err := s.lock(context.Background(), "generation-"+id)
	if err != nil {
		return err
	}
	defer releaseGeneration()
	owner = strings.TrimSpace(owner)
	if err = validateReferenceOwner(owner); err != nil {
		return err
	}
	refDir := filepath.Join(s.Root, "refs", id)
	entries, err := os.ReadDir(refDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	removed := false
	for _, entry := range entries {
		if entry.IsDir() {
			return errors.New("toolchain generation reference directory contains an invalid entry")
		}
		if validReferenceName(entry.Name(), ".tmp") {
			continue
		}
		if !validReferenceName(entry.Name(), ".ref") {
			return errors.New("toolchain generation reference directory contains an invalid entry")
		}
		path := filepath.Join(refDir, entry.Name())
		actualOwner, readErr := readReferenceOwner(path)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		if actualOwner != owner {
			continue
		}
		fd, openErr := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return openErr
		}
		lockErr := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(lockErr, unix.EWOULDBLOCK) {
			_ = unix.Close(fd)
			continue
		}
		if lockErr != nil {
			_ = unix.Close(fd)
			return lockErr
		}
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removed = true
	}
	if removed {
		if err = syncDirectory(refDir); err != nil {
			return err
		}
	}
	_ = os.Remove(refDir)
	return nil
}

func (s GenerationStore) InUse(id string) (bool, error) {
	if err := s.validate(); err != nil {
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
		if entry.IsDir() {
			return false, errors.New("toolchain generation reference directory contains an invalid entry")
		}
		if validReferenceName(entry.Name(), ".tmp") {
			continue
		}
		if !validReferenceName(entry.Name(), ".ref") {
			return false, errors.New("toolchain generation reference directory contains an invalid entry")
		}
		if _, err = readReferenceOwner(filepath.Join(refDir, entry.Name())); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return false, err
		}
		return true, nil
	}
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
