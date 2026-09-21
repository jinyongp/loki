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
	PrivateSocket, PublicSocket, Key string
	Grant                            SSHSignatureGrant
	SocketGID                        int
	Ready                            func() error
}

// RunAgent owns the private SSH agent and its restricted public proxy together.
// Existing socket paths are left intact; the service manager owns stale-runtime
// directory cleanup before this role starts.
func RunAgent(ctx context.Context, o AgentOptions) error {
	if !filepath.IsAbs(o.Key) || !filepath.IsAbs(o.PrivateSocket) || !filepath.IsAbs(o.PublicSocket) ||
		filepath.Clean(o.PrivateSocket) == filepath.Clean(o.PublicSocket) || !o.Grant.valid || o.SocketGID < 0 {
		return errors.New("invalid signing agent layout or grant")
	}
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
		if _, err = os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
			return errors.New("signing socket already exists or cannot be inspected")
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
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	agent := exec.CommandContext(run, "/usr/bin/ssh-agent", "-D", "-a", o.PrivateSocket)
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
	add := exec.CommandContext(load, "/usr/bin/ssh-add", "/proc/self/fd/3")
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
	if err = os.Chown(o.PublicSocket, -1, o.SocketGID); err != nil {
		return err
	}
	if err = os.Chmod(o.PublicSocket, 0660); err != nil {
		return err
	}
	proxy := Proxy{PrivateSocket: o.PrivateSocket, Grant: o.Grant, AgentUID: uint32(os.Getuid())}
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
