package network

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestTCPForwardRelaysAndStops(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- RunTCPForward(ctx, listener, upstream.Addr().String()) }()

	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write([]byte("authenticated payload")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("authenticated payload"))
	if _, err = io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "authenticated payload" {
		t.Fatalf("response = %q", response)
	}
	_ = conn.Close()
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown timeout")
	}
}

func TestTCPForwardRejectsInvalidTarget(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = RunTCPForward(t.Context(), listener, "missing-port"); err == nil {
		t.Fatal("invalid target accepted")
	}
}
