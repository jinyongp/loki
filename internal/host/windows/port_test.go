package windows

import (
	"net"
	"testing"
)

func TestLoopbackPortProbe(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	available, err := (LoopbackPortProbe{}).Available(port)
	if err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatal("occupied port reported available")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	available, err = (LoopbackPortProbe{}).Available(port)
	if err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("released port reported unavailable")
	}
}
