package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/config"
	"loki/internal/portguard"
)

func TestMCPRoleReadinessAndCancellation(t *testing.T) {
	c, _ := config.Parse(nil)
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c.Port = listener.Addr().(*net.TCPAddr).Port
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	browser := browserFixture(func(context.Context, string, map[string]any) (map[string]any, error) { return map[string]any{}, nil })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunMCP(ctx, c, MCPOptions{Runtime: runtime, PortGuard: runtime, Browser: browser, Jobs: jobControllerFixture(), GitRunner: gitRunnerFixture(t, c, nil), Ports: ports, Policy: policyGenerationFixture(t), Token: strings.Repeat("t", 43)}, listener, func() error { close(ready); return nil })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("readiness timeout")
	}
	response, err := http.Get("http://" + listener.Addr().String() + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal(response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
	if conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second); err == nil {
		conn.Close()
		t.Fatal("listener retained")
	}
}
