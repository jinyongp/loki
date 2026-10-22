//go:build linux

package secretbroker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/platform/privateio"
	"loki/internal/platform/safeio"
)

// RuntimeSocket is host-owned role configuration, never a client argument.
type RuntimeSocket struct {
	Path   string `json:"path"`
	Group  uint32 `json:"group"`
	Public bool   `json:"public"`
}

type runtimeOwner struct {
	Version    int             `json:"version"`
	Deployment string          `json:"deployment"`
	Runner     string          `json:"runner"`
	Instance   string          `json:"instance"`
	Sockets    []RuntimeSocket `json:"sockets"`
}

type RuntimeLease struct {
	file       *os.File
	InstanceID string
}

func (l *RuntimeLease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

// AcquireRuntime serializes cooperating broker instances through a stable
// close-on-exec lock inode. Its atomic metadata marker proves which role paths
// an interrupted instance owned. Unknown or live endpoints are never removed.
func AcquireRuntime(root, deployment, runner string, sockets []RuntimeSocket) (*RuntimeLease, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || deployment == "" || len(deployment) > 128 || !uuidPattern.MatchString(runner) || len(sockets) == 0 || len(sockets) > 3 {
		return nil, errWire
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, errWire
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(fd, &st) != nil || unix.Fstatfs(fd, &fs) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700 || fs.Type != unix.TMPFS_MAGIC {
		return nil, errWire
	}
	lockFD, err := unix.Openat(fd, "broker.lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errWire
	}
	lock := os.NewFile(uintptr(lockFD), "secret-runtime-lock")
	fail := func() (*RuntimeLease, error) { lock.Close(); return nil, errWire }
	if unix.Fstat(lockFD, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Size != 0 || unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return fail()
	}
	seen := map[string]bool{}
	for _, socket := range sockets {
		if seen[socket.Path] || !filepath.IsAbs(socket.Path) || filepath.Clean(socket.Path) != socket.Path || len(socket.Path) >= 108 {
			return fail()
		}
		seen[socket.Path] = true
	}
	marker := filepath.Join(root, "owner.json")
	previous, err := readRuntimeOwner(marker)
	known := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail()
	}
	if known && (previous.Version != 1 || previous.Deployment != deployment || previous.Runner != runner || !uuidPattern.MatchString(previous.Instance) || !reflect.DeepEqual(previous.Sockets, sockets)) {
		return fail()
	}
	// Publish ownership before binding any role path. If this process dies
	// between bind and readiness, its successor can still identify stale paths.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fail()
	}
	nonce[6] = nonce[6]&15 | 64
	nonce[8] = nonce[8]&63 | 128
	id := hex.EncodeToString(nonce[:])
	instance := id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	for _, socket := range sockets {
		if err := clearRuntimeSocket(socket, known); err != nil {
			return fail()
		}
	}
	body, _ := json.Marshal(runtimeOwner{Version: 1, Deployment: deployment, Runner: runner, Instance: instance, Sockets: sockets})
	if safeio.PublishPrivate(marker, body, true) != nil {
		return fail()
	}
	return &RuntimeLease{file: lock, InstanceID: instance}, nil
}

func readRuntimeOwner(path string) (runtimeOwner, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return runtimeOwner{}, err
	}
	f := os.NewFile(uintptr(fd), "secret-runtime-owner")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return runtimeOwner{}, errWire
	}
	body, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(body) > 16384 {
		return runtimeOwner{}, errWire
	}
	var owner runtimeOwner
	if privateio.Decode(body, &owner) != nil {
		return runtimeOwner{}, errWire
	}
	return owner, nil
}

func clearRuntimeSocket(socket RuntimeSocket, known bool) error {
	parent := filepath.Dir(socket.Path)
	fd, err := unix.Openat2(unix.AT_FDCWD, parent, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return errWire
	}
	defer unix.Close(fd)
	parentMode, socketMode := uint32(0700), uint32(0600)
	if socket.Public {
		parentMode, socketMode = 0750, 0660
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Gid != socket.Group || st.Mode&07777 != parentMode {
		return errWire
	}
	name := filepath.Base(socket.Path)
	if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return errWire
	}
	mode := st.Mode & 07777
	finalMode := mode == socketMode && st.Gid == socket.Group
	// A kill between bind, chown and chmod leaves a mode-0600 metadata
	// endpoint. It is still exclusively service-owned and safe to classify.
	initialMode := socket.Public && mode == 0600 && (st.Gid == socket.Group || st.Gid == uint32(os.Getgid()))
	if !known || st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Uid != uint32(os.Geteuid()) || !finalMode && !initialMode {
		return errWire
	}
	conn, err := net.DialTimeout("unix", socket.Path, 100*time.Millisecond)
	if err == nil {
		conn.Close()
		return errWire
	}
	if !errors.Is(err, unix.ECONNREFUSED) && !errors.Is(err, unix.ENOENT) {
		return errWire
	}
	var current unix.Stat_t
	if err := unix.Fstatat(fd, name, &current, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return errWire
	}
	if current.Dev != st.Dev || current.Ino != st.Ino {
		return errWire
	}
	return unix.Unlinkat(fd, name, 0)
}
