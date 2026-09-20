package browser

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var uploadTokenPattern = regexp.MustCompile("^[0-9a-f]{32}$")

type stagedUploadRef struct {
	Token string
	Name  string
}

type uploadStore struct {
	root          string
	inbox         string
	ownerUID      uint32
	maxFiles      int
	maxBytes      int64
	retainedFiles int
	retainedBytes int64
	directories   []string
}

type preparedUpload struct {
	directory string
	paths     []string
	bytes     int64
}

func prepareUploadDirectory(path string, mode os.FileMode) error {
	if !filepath.IsAbs(path) {
		return errors.New("browser upload directory must be absolute")
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("browser upload path must be a directory without symlinks")
	}
	return os.Chmod(path, mode)
}

func prepareUploadInbox(path string) error {
	parent := filepath.Dir(path)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return err
	}
	stat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("browser upload inbox parent has no ownership metadata")
	}
	if err = prepareUploadDirectory(path, 0o770); err != nil {
		return err
	}
	if err = os.Chown(path, os.Getuid(), int(stat.Gid)); err != nil {
		return err
	}
	return os.Chmod(path, os.ModeSetgid|0o770)
}

func newUploadStore(root, inbox string, ownerUID uint32, maxFiles, maxBytes int) (*uploadStore, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(inbox) || maxFiles < 1 || maxBytes < 1 {
		return nil, errors.New("invalid browser upload staging configuration")
	}
	if err := prepareUploadDirectory(root, 0o700); err != nil {
		return nil, err
	}
	if err := prepareUploadInbox(inbox); err != nil {
		return nil, err
	}
	store := &uploadStore{
		root: root, inbox: inbox, ownerUID: ownerUID,
		maxFiles: maxFiles, maxBytes: int64(maxBytes),
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "upload-") {
			if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	inboxEntries, err := os.ReadDir(inbox)
	if err != nil {
		return nil, err
	}
	for _, entry := range inboxEntries {
		if uploadTokenPattern.MatchString(entry.Name()) {
			if err = os.Remove(filepath.Join(inbox, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	return store, nil
}

func validUploadName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		utf8.ValidString(name) && len([]byte(name)) <= 255 &&
		!strings.ContainsAny(name, "/\\\x00")
}

func stagedUploadRefs(args map[string]any) ([]stagedUploadRef, error) {
	raw, ok := args["staged_files"]
	if !ok || raw == nil {
		return nil, errors.New("staged_files are required for browser upload")
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, errors.New("staged_files must be a non-empty array")
	}
	refs := make([]stagedUploadRef, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("staged_files must contain token/name objects")
		}
		token, tokenOK := row["token"].(string)
		name, nameOK := row["name"].(string)
		if len(row) != 2 || !tokenOK || !nameOK || !uploadTokenPattern.MatchString(token) ||
			seen[token] || !validUploadName(name) {
			return nil, errors.New("invalid staged browser upload reference")
		}
		seen[token] = true
		refs = append(refs, stagedUploadRef{Token: token, Name: name})
	}
	return refs, nil
}

func (s *uploadStore) openInbox() (int, error) {
	fd, err := unix.Open(s.inbox, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	return fd, nil
}

func (s *uploadStore) openToken(directory int, token string) (*os.File, int64, error) {
	fd, err := unix.Openat2(directory, token, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, 0, err
	}
	file := os.NewFile(uintptr(fd), token)
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		file.Close()
		return nil, 0, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != s.ownerUID || stat.Nlink != 1 || stat.Mode&0o007 != 0 {
		file.Close()
		return nil, 0, errors.New("staged browser upload file failed ownership or file-type validation")
	}
	return file, stat.Size, nil
}

func (s *uploadStore) Prepare(refs []stagedUploadRef) (_ *preparedUpload, err error) {
	if s == nil {
		return nil, errors.New("browser file upload is not configured")
	}
	if len(refs) == 0 || len(refs) > s.maxFiles || s.retainedFiles+len(refs) > s.maxFiles {
		return nil, errors.New("browser upload file-count limit exceeded")
	}
	inbox, err := s.openInbox()
	if err != nil {
		return nil, err
	}
	defer unix.Close(inbox)

	directory, err := os.MkdirTemp(s.root, "upload-")
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	prepared := &preparedUpload{directory: directory}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()

	for index, ref := range refs {
		source, size, openErr := s.openToken(inbox, ref.Token)
		if openErr != nil {
			return nil, openErr
		}
		remaining := s.maxBytes - s.retainedBytes - prepared.bytes
		if remaining < 0 || size > remaining {
			source.Close()
			return nil, errors.New("browser upload byte limit exceeded")
		}
		subdir := filepath.Join(directory, fmt.Sprintf("%03d", index))
		if err = os.Mkdir(subdir, 0o700); err != nil {
			source.Close()
			return nil, err
		}
		destinationPath := filepath.Join(subdir, ref.Name)
		destination, createErr := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			source.Close()
			return nil, createErr
		}
		copied, copyErr := io.Copy(destination, io.LimitReader(source, remaining+1))
		sourceErr := source.Close()
		syncErr := destination.Sync()
		closeErr := destination.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if sourceErr != nil {
			return nil, sourceErr
		}
		if syncErr != nil {
			return nil, syncErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if copied > remaining || copied != size {
			return nil, errors.New("browser upload byte limit exceeded or staged file changed during copy")
		}
		if err = unix.Unlinkat(inbox, ref.Token, 0); err != nil {
			return nil, err
		}
		prepared.bytes += copied
		prepared.paths = append(prepared.paths, destinationPath)
	}
	return prepared, nil
}

func (s *uploadStore) Commit(prepared *preparedUpload) {
	if s == nil || prepared == nil {
		return
	}
	s.retainedFiles += len(prepared.paths)
	s.retainedBytes += prepared.bytes
	s.directories = append(s.directories, prepared.directory)
}

func (s *uploadStore) Discard(prepared *preparedUpload) {
	if prepared != nil {
		_ = os.RemoveAll(prepared.directory)
	}
}

func (s *uploadStore) Clear() {
	if s == nil {
		return
	}
	for _, directory := range s.directories {
		_ = os.RemoveAll(directory)
	}
	s.directories = nil
	s.retainedFiles = 0
	s.retainedBytes = 0
}
