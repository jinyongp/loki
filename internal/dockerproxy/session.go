// Package dockerproxy provides a disposable socket for an approved Docker action.
// Approval grants Docker API access; this transport does not filter API methods.
package dockerproxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/rpc"
)

type Config struct {
	Directory, Upstream               string
	RunnerUID, RunnerGID, UpstreamUID uint32
}
type Session struct {
	Path              string
	parent, directory *os.File
	name              string
	listener          *net.UnixListener
	cancel            context.CancelFunc
	once              sync.Once
	workers           sync.WaitGroup
}

func Start(config Config) (*Session, error) {
	if !filepath.IsAbs(config.Directory) || !filepath.IsAbs(config.Upstream) {
		return nil, errors.New("Docker proxy requires absolute configured paths")
	}
	parent, err := unix.Openat2(unix.AT_FDCWD, config.Directory, &unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			unix.Close(parent)
		}
	}()
	var owner unix.Stat_t
	if err := unix.Fstat(parent, &owner); err != nil {
		return nil, err
	}
	if owner.Uid != uint32(os.Geteuid()) || owner.Mode&0022 != 0 {
		return nil, errors.New("Docker proxy root has an unexpected owner")
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, err
	}
	name := "docker-" + hex.EncodeToString(suffix[:])
	if err := unix.Mkdirat(parent, name, 0700); err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
		}
	}()
	dir, err := unix.Openat2(parent, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			unix.Close(dir)
		}
	}()
	if err := unix.Fchown(dir, -1, int(config.RunnerGID)); err != nil {
		return nil, err
	}
	if err := unix.Fchmod(dir, 0710); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: fmt.Sprintf("/proc/self/fd/%d/docker.sock", dir), Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	defer func() {
		if !ok {
			listener.Close()
			unix.Unlinkat(dir, "docker.sock", 0)
		}
	}()
	if err := unix.Fchownat(dir, "docker.sock", -1, int(config.RunnerGID), unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if err := unix.Fchmodat(dir, "docker.sock", 0660, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &Session{Path: filepath.Join(config.Directory, name, "docker.sock"), parent: os.NewFile(uintptr(parent), "Docker proxy root"), directory: os.NewFile(uintptr(dir), "Docker action session"), name: name, listener: listener, cancel: cancel}
	session.workers.Go(func() { session.serve(ctx, config) })
	ok = true
	return session, nil
}
func (s *Session) Close() error {
	var result error
	s.once.Do(func() {
		s.cancel()
		s.listener.Close()
		s.workers.Wait()
		result = errors.Join(unix.Unlinkat(int(s.directory.Fd()), "docker.sock", 0), unix.Unlinkat(int(s.parent.Fd()), s.name, unix.AT_REMOVEDIR))
		s.directory.Close()
		s.parent.Close()
	})
	return result
}

// Socket returns a caller-owned descriptor for the sandbox bind mount.
func (s *Session) Socket() (*os.File, error) {
	fd, err := unix.Openat2(int(s.directory.Fd()), "docker.sock", &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		unix.Close(fd)
		return nil, errors.New("Docker proxy socket was replaced")
	}
	return os.NewFile(uintptr(fd), "Docker action socket"), nil
}
func (s *Session) serve(ctx context.Context, config Config) {
	slots := make(chan struct{}, 64)
	for {
		client, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
			s.workers.Go(func() { defer func() { <-slots }(); defer client.Close(); handle(ctx, config, client) })
		default:
			client.Close()
		}
	}
}
func handle(ctx context.Context, config Config, client *net.UnixConn) {
	peer, err := rpc.PeerCredentials(client)
	if err != nil || peer.UID != config.RunnerUID {
		return
	}
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	upstream, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", config.Upstream)
	if err != nil {
		return
	}
	defer upstream.Close()
	peer, err = rpc.PeerCredentials(upstream.(*net.UnixConn))
	if err != nil || peer.UID != config.UpstreamUID {
		return
	}
	stopUpstream := context.AfterFunc(ctx, func() { upstream.Close() })
	defer stopUpstream()
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, client); upstream.(*net.UnixConn).CloseWrite(); done <- struct{}{} }()
	go func() { io.Copy(client, upstream); client.CloseWrite(); done <- struct{}{} }()
	<-done
	<-done
}
