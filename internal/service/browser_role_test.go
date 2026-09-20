package service

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loki/internal/browser"
)

type browserFixture func(context.Context, string, map[string]any) (map[string]any, error)

func (f browserFixture) Call(ctx context.Context, op string, args map[string]any) (map[string]any, error) {
	return f(ctx, op, args)
}
func TestBrowserOperationEnvelope(t *testing.T) {
	caller := browserFixture(func(_ context.Context, op string, args map[string]any) (map[string]any, error) {
		return map[string]any{"operation": op, "arguments": args}, nil
	})
	ops := BrowserOperations(caller)
	expected := []string{"start", "navigate", "state", "click", "hover", "drag", "wheel", "fill", "type", "key", "shortcut", "select_option", "set_checked", "focus", "upload", "dialog_state", "handle_dialog", "back", "list_tabs", "switch_tab", "close_tab", "screenshot", "console", "network", "request", "websockets", "page_errors", "debug_diagnostics", "stop"}
	if len(ops) != len(expected) {
		t.Fatalf("browser operations = %d, want %d", len(ops), len(expected))
	}
	for _, name := range expected {
		if _, ok := ops[name]; !ok {
			t.Errorf("browser operation %s missing", name)
		}
	}
	for _, removed := range []string{"press", "scroll"} {
		if _, ok := ops[removed]; ok {
			t.Errorf("legacy browser operation %s remains", removed)
		}
	}
	for name, op := range ops {
		result, err := op.Handle(t.Context(), json.RawMessage(`{"arguments":{"index":12}}`))
		if err != nil {
			t.Fatal(err)
		}
		row := result.(map[string]any)
		if row["operation"] != name || row["arguments"].(map[string]any)["index"] != json.Number("12") {
			t.Fatal(row)
		}
		for _, raw := range []string{`{"arguments":null}`, `{"arguments":[]}`, `{"arguments":"text"}`} {
			if _, err := op.Handle(t.Context(), []byte(raw)); err == nil {
				t.Fatal(raw)
			}
		}
	}
}
func TestBrowserRPCReportsMissingOptionalSocket(t *testing.T) {
	client := NewBrowserRPC(filepath.Join(t.TempDir(), "browser.sock"), uint32(os.Getuid()))
	if _, err := client.Call(t.Context(), "state", nil); err == nil || err.Error() != "browser is not configured" {
		t.Fatalf("missing browser error = %v", err)
	}
}

func TestBrowserRPCReportsStaleSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "browser.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	listener.Close()
	client := NewBrowserRPC(socket, uint32(os.Getuid()))
	if _, err = client.Call(t.Context(), "state", nil); err == nil || err.Error() != "browser is unavailable" {
		t.Fatalf("stale browser error = %v", err)
	}
}

func TestBrowserRoleLifecycle(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "socket", "browser.sock")
	uid := uint32(os.Getuid())
	binary := os.Getenv("LOKI_TEST_CHROME")
	if binary == "" {
		binary = "/unneeded-until-start"
	}
	options := BrowserOptions{Socket: socket, AgentUID: uid, SocketGID: os.Getgid(), Browser: browser.Options{Binary: binary, Profile: filepath.Join(root, "profile"), Downloads: filepath.Join(root, "downloads"), Proxy: "http://127.0.0.1:1", LibraryPath: os.Getenv("LOKI_TEST_CHROME_LIBS")}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- RunBrowser(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("readiness timeout")
	}
	client := NewBrowserRPC(socket, uid)
	if result, err := client.Call(t.Context(), "stop", nil); err != nil || result["status"] != "stopped" {
		t.Fatal(result, err)
	}
	if os.Getenv("LOKI_TEST_CHROME") != "" {
		if _, err := client.Call(t.Context(), "start", nil); err != nil {
			t.Fatal(err)
		}
		if result, err := client.Call(t.Context(), "screenshot", nil); err != nil || result["mime_type"] != "image/png" {
			t.Fatal(err)
		}
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
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatal("socket retained", err)
	}
}
