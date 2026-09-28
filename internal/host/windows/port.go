package windows

import (
	"fmt"
	"net"
)

type LoopbackPortProbe struct{}

func (LoopbackPortProbe) Available(port int) (bool, error) {
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		if isSocketAddressInUse(err) {
			return false, nil
		}
		return false, err
	}
	return true, listener.Close()
}
