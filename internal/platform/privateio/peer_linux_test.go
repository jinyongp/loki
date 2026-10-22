//go:build linux

package privateio

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPeerChecksKernelUIDBeforeProtocolInput(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "loki-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	address := &net.UnixAddr{Name: filepath.Join(dir, "s"), Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_ = listener.SetDeadline(time.Now().Add(time.Second))
	client, err := net.DialUnix("unix", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	uid := uint32(os.Getuid())
	if VerifyPeer(server, uid) != nil || VerifyPeer(client, uid) != nil {
		t.Fatal("valid mutual kernel peer rejected")
	}
	if VerifyPeer(server, uid+1) == nil || VerifyPeer(client, uid+1) == nil {
		t.Fatal("foreign kernel peer authorized")
	}
}
