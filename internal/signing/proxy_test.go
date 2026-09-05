package signing

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func listener(t *testing.T) *net.UnixListener {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "agent.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}
func packet(payload ...byte) []byte {
	data := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(data, uint32(len(payload)))
	copy(data[4:], payload)
	return data
}

func TestProxyAllowedRequestsAndUID(t *testing.T) {
	for _, mode := range []string{"allowed", "client-uid", "agent-uid"} {
		t.Run(mode, func(t *testing.T) {
			private, public := listener(t), listener(t)
			uid := uint32(os.Getuid())
			proxy := Proxy{PrivateSocket: private.Addr().String(), RunnerUID: uid, AgentUID: uid, Timeout: time.Second}
			if mode == "client-uid" {
				proxy.RunnerUID++
			}
			if mode == "agent-uid" {
				proxy.AgentUID++
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- proxy.Serve(ctx, public) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			agentDone := make(chan struct{})
			go func() {
				defer close(agentDone)
				conn, err := private.AcceptUnix()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				for _, want := range []byte{11, 13} {
					request, err := frame(conn, false)
					if err != nil {
						return
					}
					if request[4] != want {
						t.Errorf("forwarded forbidden request %d", request[4])
						return
					}
					if err := write(conn, packet(want+1)); err != nil {
						return
					}
				}
			}()
			t.Cleanup(func() { private.Close(); <-agentDone })
			conn, err := net.Dial("unix", public.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			if mode != "allowed" {
				write(conn, packet(11))
				if _, err := frame(conn, false); err == nil {
					t.Fatal("wrong UID accepted")
				}
				return
			}
			for _, operation := range []byte{17, 18, 19, 20, 22, 23, 25, 27} {
				if err := write(conn, packet(operation)); err != nil {
					t.Fatal(err)
				}
				reply, err := frame(conn, false)
				if err != nil || !bytes.Equal(reply, failure) {
					t.Fatalf("forbidden operation %d: %v %v", operation, reply, err)
				}
			}
			for _, operation := range []byte{11, 13} {
				if err := write(conn, packet(operation)); err != nil {
					t.Fatal(err)
				}
				reply, err := frame(conn, false)
				if err != nil || !bytes.Equal(reply, packet(operation+1)) {
					t.Fatalf("allowed operation: %v %v", reply, err)
				}
			}
		})
	}
}
func TestFrameBounds(t *testing.T) {
	for _, size := range []uint32{0, MaxMessage + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		if _, err := frame(bytes.NewReader(header[:]), false); err == nil {
			t.Fatal(size)
		}
	}
	if _, err := frame(bytes.NewReader([]byte{0, 0, 0, 2, 11}), false); err == nil {
		t.Fatal("truncated frame")
	}
}
