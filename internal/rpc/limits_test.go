package rpc

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIndependentRequestResponseLimits(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "browser.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	limits := Limits{RequestBytes: 1024, ResponseBytes: 3 * 1024 * 1024, Timeout: time.Second}
	server := Server{AgentUID: uint32(os.Getuid()), Limits: limits, Operations: map[string]Operation{
		"large": {Handle: func(context.Context, json.RawMessage) (any, error) { return strings.Repeat("x", MaxBytes), nil }},
		"oversized": {Handle: func(context.Context, json.RawMessage) (any, error) {
			return strings.Repeat("x", limits.ResponseBytes), nil
		}},
		"wait": {Handle: func(ctx context.Context, _ json.RawMessage) (any, error) { <-ctx.Done(); return nil, ctx.Err() }},
	}}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client := Client{Socket: socket, Limits: limits}
	if result, err := client.Call(ctx, map[string]string{"operation": "large"}); err != nil || len(result) <= MaxBytes {
		t.Fatal(len(result), err)
	}
	if _, err := (Client{Socket: socket}).Call(ctx, map[string]string{"operation": "large"}); err == nil {
		t.Fatal("default response bound was relaxed")
	}
	for _, request := range []map[string]string{{"operation": "oversized"}, {"operation": "large", "padding": strings.Repeat("x", 1024)}} {
		if _, err := client.Call(ctx, request); err == nil {
			t.Fatal("bound bypassed")
		}
	}
	// A larger client allowance cannot bypass the server's request bound.
	if _, err := (Client{Socket: socket}).Call(ctx, map[string]string{"operation": "large", "padding": strings.Repeat("x", 1024)}); err == nil {
		t.Fatal("server accepted oversized request")
	}
	short := client
	short.Limits.Timeout = 25 * time.Millisecond
	started := time.Now()
	if _, err := short.Call(ctx, map[string]string{"operation": "wait"}); err == nil || time.Since(started) > time.Second {
		t.Fatal("deadline was not enforced", err)
	}
}
