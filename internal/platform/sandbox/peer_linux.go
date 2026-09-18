//go:build linux

package sandbox

import (
	"net"

	"golang.org/x/sys/unix"
)

func unixPeerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *unix.Ucred
	var inner error
	if err = raw.Control(func(fd uintptr) {
		credentials, inner = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if inner != nil {
		return 0, inner
	}
	return credentials.Uid, nil
}
