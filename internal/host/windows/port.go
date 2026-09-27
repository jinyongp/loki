package windows

import (
	"errors"
	"fmt"
	"net"
	"syscall"
)

type LoopbackPortProbe struct{}

func (LoopbackPortProbe) Available(port int) (bool, error) {
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return false, nil
		}
		return false, err
	}
	return true, listener.Close()
}
