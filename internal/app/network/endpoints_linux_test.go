//go:build linux

package network

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func endpointFixture(t *testing.T, validate func(context.Context, int) (string, error)) EndpointDialer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "endpoints.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- RunOwnedEndpoints(ctx, listener, []uint32{uint32(os.Getuid())}, validate, func() error { return nil }, nil)
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("owned endpoint role did not shut down")
		}
	})
	return EndpointDialer{Socket: path, UID: uint32(os.Getuid())}
}

func TestOwnedEndpointRelayRejectsUnownedPortsAndUnexpectedPeer(t *testing.T) {
	var calls atomic.Int32
	dialer := endpointFixture(t, func(context.Context, int) (string, error) { calls.Add(1); return "", errors.New("unowned endpoint") })
	if connection, err := dialer.Dial(t.Context(), 43001); err == nil {
		connection.Close()
		t.Fatal("unowned loopback port became reachable")
	}
	if calls.Load() != 1 {
		t.Fatal("rejected endpoint was not checked once before dialing")
	}
	dialer.UID++
	if connection, err := dialer.Dial(t.Context(), 0); err == nil {
		connection.Close()
		t.Fatal("unexpected Unix peer identity accepted")
	}
}

func TestOwnedEndpointRelayClosesExistingConnectionAfterLeaseRevocation(t *testing.T) {
	upstream, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upstream.Close() })
	port := upstream.Addr().(*net.TCPAddr).Port
	accepted := make(chan struct{})
	go func() {
		connection, err := upstream.AcceptTCP()
		if err != nil {
			return
		}
		defer connection.Close()
		close(accepted)
		io.Copy(connection, connection)
	}()
	var revoked atomic.Bool
	dialer := endpointFixture(t, func(_ context.Context, requested int) (string, error) {
		if requested != port || revoked.Load() {
			return "", errors.New("released endpoint")
		}
		return "immutable-instance-lease", nil
	})
	connection, err := dialer.Dial(t.Context(), port)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("owned endpoint was not connected")
	}
	connection.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := connection.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	var echoed [5]byte
	if _, err := io.ReadFull(connection, echoed[:]); err != nil || string(echoed[:]) != "hello" {
		t.Fatal("owned endpoint did not relay application bytes")
	}
	revoked.Store(true)
	connection.SetReadDeadline(time.Now().Add(4 * time.Second))
	if _, err := connection.Read(echoed[:]); err == nil {
		t.Fatal("revoked tunnel remained open")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("revocation did not close the existing connection")
	}
}
