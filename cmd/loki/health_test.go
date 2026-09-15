package main

import (
	"bytes"
	"net"
	"path/filepath"
	"testing"
)

func TestHealthRequiresLiveUnixAndLoopbackTCP(t *testing.T) {
	unixPath := filepath.Join(t.TempDir(), "service.sock")
	unixListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: unixPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if code := runHealth([]string{"--unix", unixPath, "--tcp", tcpListener.Addr().String()}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("health code = %d", code)
	}
	unixListener.SetUnlinkOnClose(false)
	unixListener.Close()
	tcpListener.Close()
	if code := runHealth([]string{"--unix", unixPath}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("stale socket health code = %d", code)
	}
	if code := runHealth([]string{"--tcp", "192.0.2.1:443"}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("external probe health code = %d", code)
	}
}
