//go:build !linux

package sandbox

import (
	"errors"
	"net"
)

func unixPeerUID(*net.UnixConn) (uint32, error) {
	return 0, errors.New("sandbox Docker peer verification requires Linux")
}
