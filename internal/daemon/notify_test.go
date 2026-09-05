package daemon

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestNotify(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = Notify(socket, "READY=1"); err != nil {
		t.Fatal(err)
	}
	listener.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 64)
	n, _, err := listener.ReadFromUnix(buffer)
	if err != nil || string(buffer[:n]) != "READY=1" {
		t.Fatal(string(buffer[:n]), err)
	}
}
