//go:build linux

package privateio

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// VerifyPeer checks the kernel identity, independently of socket file access.
// Both ends of each private channel call it before reading or sending frames.
func VerifyPeer(conn *net.UnixConn, expected uint32) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return errors.New("private peer is unavailable")
	}
	var credentials *unix.Ucred
	var lookup error
	err = raw.Control(func(fd uintptr) {
		credentials, lookup = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil || lookup != nil || credentials == nil || credentials.Uid != expected {
		return errors.New("private peer is not authorized")
	}
	return nil
}
