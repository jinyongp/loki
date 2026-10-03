package signing

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type AgentOptions struct {
	Authorize                        func(context.Context) error
	PrivateSocket, PublicSocket, Key string
	AgentBinary, AddBinary           string
	Grant                            SSHSignatureGrant
	SocketUID, SocketGID             int
	Ready                            func() error
}

// RunAgent owns the private SSH agent and its restricted public proxy together.
// Directory locks exclude concurrent agents while verified stale sockets are
// reclaimed after an unclean shutdown.
func RunAgent(ctx context.Context, o AgentOptions) error {
	if o.Authorize != nil {
		if err := o.Authorize(ctx); err != nil {
			return err
		}
	}
	if o.AgentBinary == "" {
		o.AgentBinary = "/usr/bin/ssh-agent"
	}
	if o.AddBinary == "" {
		o.AddBinary = "/usr/bin/ssh-add"
	}
	if !filepath.IsAbs(o.AgentBinary) || !filepath.IsAbs(o.AddBinary) {
		return errors.New("signing requires administrator-owned absolute OpenSSH executables")
	}
	if !filepath.IsAbs(o.Key) || !filepath.IsAbs(o.PrivateSocket) || !filepath.IsAbs(o.PublicSocket) ||
		filepath.Clean(o.PrivateSocket) == filepath.Clean(o.PublicSocket) || !o.Grant.valid || o.SocketUID < 0 || o.SocketGID < 0 {
		return errors.New("invalid signing agent layout or grant")
	}
	parents := map[string]struct{}{}
	var locks []int
	defer func() {
		for _, fd := range locks {
			_ = unix.Close(fd)
		}
	}()
	for _, socket := range []string{o.PrivateSocket, o.PublicSocket} {
		parent := filepath.Dir(socket)
		resolved, err := filepath.EvalSymlinks(parent)
		if err != nil || resolved != parent {
			return errors.New("signing socket directory must not contain symlinks")
		}
		var stat unix.Stat_t
		if err = unix.Stat(parent, &stat); err != nil || stat.Uid != uint32(os.Getuid()) || stat.Mode&0022 != 0 {
			return errors.New("signing socket directory must be service-owned and not writable by other users")
		}
		if _, ok := parents[parent]; !ok {
			fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return errors.New("cannot lock signing socket directory")
			}
			locks = append(locks, fd)
			if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
				return errors.New("signing socket directory is already in use")
			}
			// Job containers intentionally do not receive the workspace
			// supplementary group. Keep the directory non-listable/non-writable
			// while allowing traversal to the public socket; the socket inode
			// remains the actual delegated-authority boundary.
			if err = os.Chmod(parent, 0711); err != nil {
				return errors.New("cannot prepare signing socket directory traversal")
			}
			parents[parent] = struct{}{}
		}
	}
	fd, err := unix.Open(o.Key, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("cannot open signing key")
	}
	key := os.NewFile(uintptr(fd), "signing-key")
	defer key.Close()
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Getuid()) || stat.Mode&0077 != 0 {
		return errors.New("signing key must be a private service-owned regular file")
	}
	if err = reclaimAgentSockets(ctx, o); err != nil {
		return err
	}
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	agent := exec.CommandContext(run, o.AgentBinary, "-D", "-a", o.PrivateSocket)
	agent.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
	if err = agent.Start(); err != nil {
		return errors.New("cannot start private signing agent")
	}
	done := make(chan struct{})
	go func() { _ = agent.Wait(); close(done) }()
	defer func() { cancel(); <-done; _ = os.Remove(o.PrivateSocket) }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		info, err := os.Lstat(o.PrivateSocket)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			return errors.New("private signing agent exited during startup")
		case <-deadline.C:
			return errors.New("private signing agent socket was not ready")
		case <-time.After(25 * time.Millisecond):
		}
	}
	if err = os.Chmod(o.PrivateSocket, 0600); err != nil {
		return err
	}
	load, cancelLoad := context.WithTimeout(run, 10*time.Second)
	defer cancelLoad()
	add := exec.CommandContext(load, o.AddBinary, "/proc/self/fd/3")
	add.ExtraFiles = []*os.File{key}
	add.Env = []string{"PATH=/usr/bin:/bin", "SSH_AUTH_SOCK=" + o.PrivateSocket, "SSH_ASKPASS_REQUIRE=never"}
	if err = add.Run(); err != nil {
		return errors.New("cannot load signing key into private agent")
	}
	public, err := net.ListenUnix("unix", &net.UnixAddr{Name: o.PublicSocket, Net: "unix"})
	if err != nil {
		return errors.New("cannot create public signing socket")
	}
	defer public.Close()
	// Apply the public mode while the service still owns the socket. The
	// container deliberately has CAP_CHOWN but not CAP_FOWNER; chmod after
	// transferring ownership to the runner would therefore fail closed.
	if err = os.Chmod(o.PublicSocket, 0660); err != nil {
		return err
	}
	if err = os.Chown(o.PublicSocket, o.SocketUID, o.SocketGID); err != nil {
		return err
	}
	proxy := Proxy{PrivateSocket: o.PrivateSocket, Grant: o.Grant, AgentUID: uint32(os.Getuid()), Authorize: o.Authorize}
	proxyDone := make(chan error, 1)
	go func() { proxyDone <- proxy.Serve(run, public) }()
	defer func() { cancel(); public.Close(); <-proxyDone }()
	if o.Ready != nil {
		if err = o.Ready(); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return nil
	case <-done:
		return errors.New("private signing agent exited")
	case err = <-proxyDone:
		proxyDone <- err
		if err == nil {
			return errors.New("signing proxy stopped")
		}
		return err
	}
}

func reclaimAgentSockets(ctx context.Context, o AgentOptions) error {
	paths := []string{o.PrivateSocket, o.PublicSocket}
	owners := []uint32{uint32(os.Getuid()), uint32(o.SocketUID)}
	stale := make(map[string]os.FileInfo)
	for i, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			return errors.New("signing socket path is occupied or cannot be inspected")
		}
		var stat unix.Stat_t
		if err = unix.Lstat(path, &stat); err != nil || (stat.Uid != owners[i] && !(i == 1 && stat.Uid == uint32(os.Getuid()))) {
			return errors.New("signing socket ownership does not match")
		}
		connection, err := (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "unix", path)
		if err == nil {
			_ = connection.Close()
			return errors.New("signing socket is already in use")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(err, unix.ECONNREFUSED) {
			return errors.New("cannot verify signing socket is stale")
		}
		stale[path] = info
	}
	for _, path := range paths {
		if info := stale[path]; info != nil {
			current, err := os.Lstat(path)
			if err != nil || !os.SameFile(info, current) {
				return errors.New("signing socket changed during recovery")
			}
			if err = os.Remove(path); err != nil {
				return errors.New("cannot remove stale signing socket")
			}
		}
	}
	return nil
}
