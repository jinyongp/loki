package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPeerPolicy(t *testing.T) {
	s := Server{AgentUID: 1000}
	s.ReadCgroup = func(int32) ([]byte, error) { return []byte("0::/system.slice/loki-mcp.service\n"), nil }
	if !s.Authorized(Peer{PID: 42, UID: 1000}, Administrative) {
		t.Fatal("MCP peer rejected")
	}
	for _, path := range []string{"0::/system.slice/loki-action-test.service", "0::/user.slice/shell.scope", "0::/system.slice/loki-mcp.service/child", "0::/system.slice/fake-loki-mcp.service"} {
		s.ReadCgroup = func(int32) ([]byte, error) { return []byte(path), nil }
		if s.Authorized(Peer{PID: 42, UID: 1000}, Administrative) {
			t.Fatalf("authorized %s", path)
		}
	}
	if !s.Authorized(Peer{UID: 0}, Administrative) || !s.Authorized(Peer{UID: 1000}, Agent) || s.Authorized(Peer{UID: 1001}, Agent) {
		t.Fatal("UID permission policy failed")
	}
	s.ReadCgroup = func(int32) ([]byte, error) { return nil, os.ErrPermission }
	if s.Authorized(Peer{PID: 42, UID: 1000}, Administrative) {
		t.Fatal("failed open on /proc error")
	}
}

func TestSocketRoundtripBoundsAndSanitization(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "runtime.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer listener.Close()
	var mu sync.Mutex
	events := []Event{}
	s := Server{AgentUID: uint32(os.Getuid()), Operations: map[string]Operation{
		"echo":    {Handle: func(ctx context.Context, raw json.RawMessage) (any, error) { return Decode[map[string]any](raw) }},
		"failure": {Handle: func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("synthetic-private-value") }},
		"panic":   {Handle: func(context.Context, json.RawMessage) (any, error) { panic("private-panic") }},
		"large":   {Handle: func(context.Context, json.RawMessage) (any, error) { return strings.Repeat("x", MaxBytes), nil }},
	}, Audit: func(e Event) { mu.Lock(); defer mu.Unlock(); events = append(events, e) }}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	uid := uint32(os.Getuid())
	c := Client{Socket: socket, ExpectedUID: &uid}
	result, err := c.Call(ctx, map[string]any{"operation": "echo", "value": "hello"})
	if err != nil || !strings.Contains(string(result), "hello") {
		t.Fatalf("roundtrip: %s %v", result, err)
	}
	for _, operation := range []string{"failure", "panic", "large", "missing"} {
		_, err := c.Call(ctx, map[string]any{"operation": operation})
		if err == nil {
			t.Fatalf("missing error %s", operation)
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatalf("error leak %s", operation)
		}
	}
	if _, err := c.Call(ctx, map[string]any{"operation": "echo", "value": strings.Repeat("x", MaxBytes)}); err == nil {
		t.Fatal("oversized request allowed")
	}
	if _, err := readFrame(strings.NewReader(strings.Repeat("x", MaxBytes+1))); err == nil {
		t.Fatal("oversized frame allowed")
	}
	wrongUID := uid + 1
	c.ExpectedUID = &wrongUID
	if _, err := c.Call(ctx, map[string]string{"operation": "echo"}); err == nil {
		t.Fatal("untrusted socket peer allowed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) < 5 {
		t.Fatalf("missing audit events: %v", events)
	}
}
