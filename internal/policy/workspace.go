// Package policy confines untrusted workspace paths before any filesystem I/O.
package policy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
)

type Workspace struct {
	root      string
	directory *os.File
}

func New(root string) (*Workspace, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		f.Close()
		return nil, fault.Error("workspace root is not a directory")
	}
	return &Workspace{root: resolved, directory: f}, nil
}

func (w *Workspace) Close() error { return w.directory.Close() }
func (w *Workspace) Root() string { return w.root }

func Relative(requested string) (string, error) {
	if strings.ContainsAny(requested, "\x00\\") {
		return "", fault.Error("invalid path")
	}
	if strings.HasPrefix(requested, "/") {
		return "", fault.Error("absolute paths are not allowed")
	}
	for _, part := range strings.Split(requested, "/") {
		if part == ".." {
			return "", fault.Error("parent traversal is not allowed")
		}
		lower := strings.ToLower(part)
		switch lower {
		case ".git", ".ssh", ".gnupg", ".env", ".env.local", ".env.production", ".netrc", ".npmrc", ".pypirc":
			return "", fault.Error("access denied: " + part)
		}
		for _, suffix := range []string{".key", ".pem", ".p12", ".pfx"} {
			if strings.HasSuffix(lower, suffix) {
				return "", fault.Error("access denied: " + part)
			}
		}
	}
	return filepath.Clean(requested), nil
}

func CWD(requested string) (string, error) {
	if requested == "/workspace" || strings.HasPrefix(requested, "/workspace/") {
		requested = strings.TrimPrefix(strings.TrimPrefix(requested, "/workspace"), "/")
	} else if strings.HasPrefix(requested, "/") {
		return "", fault.Error("absolute cwd must be within /workspace")
	}
	return Relative(requested)
}

// Open pins the root directory and rejects symlinks atomically, including
// links swapped in after lexical checks. No fallback weakens this boundary.
func (w *Workspace) Open(requested string, flags int, mode fs.FileMode) (*os.File, error) {
	relative, err := Relative(requested)
	if err != nil {
		return nil, err
	}
	openFlags := flags | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if flags&unix.O_PATH == 0 {
		openFlags |= unix.O_NONBLOCK
	}
	how := &unix.OpenHow{Flags: uint64(openFlags), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS}
	if flags&os.O_CREATE != 0 {
		how.Mode = uint64(mode.Perm())
	}
	fd, err := unix.Openat2(int(w.directory.Fd()), relative, how)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fault.Error("symbolic links are not allowed")
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), relative)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		f.Close()
		return nil, fault.Error("special files are not allowed")
	}
	return f, nil
}

// Resolve supplies a confined display/command path. File reads and writes use
// Open or a pinned parent, never the result of a check-then-open sequence.
func (w *Workspace) Resolve(requested string, mustExist bool) (string, error) {
	relative, err := Relative(requested)
	if err != nil {
		return "", err
	}
	current := "."
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		f, err := w.Open(current, unix.O_PATH, 0)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && !mustExist {
				continue
			}
			return "", err
		}
		f.Close()
	}
	return filepath.Join(w.root, relative), nil
}

func (w *Workspace) ResolveCWD(requested string) (string, error) {
	relative, err := CWD(requested)
	if err != nil {
		return "", err
	}
	f, err := w.Open(relative, os.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return "", err
	}
	f.Close()
	return filepath.Join(w.root, relative), nil
}
