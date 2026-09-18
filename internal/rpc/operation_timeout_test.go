package rpc

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
)

func TestAuthorizedOperationDeadline(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "rpc.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := func(ctx context.Context, _ json.RawMessage) (any, error) {
		select {
		case <-time.After(150 * time.Millisecond):
			return "finished", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	server := Server{Principals: identity.UnixResolver{AgentUID: uint32(os.Getuid())}, Limits: Limits{Timeout: 50 * time.Millisecond}, Operations: map[string]Operation{
		"long":    {Grant: controlpolicy.Agent, Timeout: time.Second, Handle: handler},
		"default": {Grant: controlpolicy.Agent, Handle: handler},
	}}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	uid := uint32(os.Getuid())
	client := Client{Socket: socket, ExpectedUID: &uid, Limits: Limits{Timeout: time.Second}}
	if _, err = client.Call(ctx, map[string]any{"operation": "long"}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Call(ctx, map[string]any{"operation": "default", "timeout_seconds": 1800}); err == nil {
		t.Fatal("request extended default execution limit")
	}
	connection, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetReadDeadline(time.Now().Add(time.Second))
	connection.Write([]byte(`{"operation":"long"`))
	buffer := make([]byte, 256)
	if _, err = connection.Read(buffer); err == nil {
		t.Fatal("unfinished request was accepted")
	}
}
