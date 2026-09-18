package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"loki/internal/platform/netguard"
)

func chromeDriver(t *testing.T, handler http.Handler) (*Driver, string) {
	t.Helper()
	binary := os.Getenv("LOKI_TEST_CHROME")
	if binary == "" {
		if os.Getenv("LOKI_REQUIRE_BROWSER_TESTS") == "1" {
			t.Fatal("LOKI_TEST_CHROME is required")
		}
		t.Skip("optional development Chromium fixture")
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	proxy := netguard.New(netguard.Policy{ValidatePort: func(_ context.Context, p int) bool { return p == port }, Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return nil, errors.New("fixture disables external DNS")
	}})
	proxyServer := httptest.NewServer(proxy)
	t.Cleanup(func() { proxy.Close(); proxyServer.Close() })
	root := t.TempDir()
	driver, err := NewDriver(Options{Binary: binary, Profile: filepath.Join(root, "profile"), Downloads: filepath.Join(root, "downloads"), Proxy: proxyServer.URL, LibraryPath: os.Getenv("LOKI_TEST_CHROME_LIBS")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	return driver, server.URL
}
func callBrowser(t *testing.T, d *Driver, operation string, args map[string]any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	result, err := d.Call(ctx, operation, args)
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	return result
}
func TestChromiumLifecycle(t *testing.T) {
	var visits atomic.Int32
	d, address := chromeDriver(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<title>%s</title><p>fixture</p>", r.URL.Path)
	}))
	if _, err := d.Call(t.Context(), "list_tabs", nil); err == nil {
		t.Fatal("accepted stopped browser")
	}
	first := callBrowser(t, d, "start", nil)["active_tab_id"]
	if first == nil {
		t.Fatal("missing tab")
	}
	if again := callBrowser(t, d, "start", nil)["active_tab_id"]; again != first {
		t.Fatal("start replaced active tab")
	}
	page := callBrowser(t, d, "navigate", map[string]any{"url": address + "/first"})
	if page["title"] != "/first" || page["url"] != address+"/first" {
		t.Fatal(page)
	}
	callBrowser(t, d, "navigate", map[string]any{"url": address + "/next"})
	if page = callBrowser(t, d, "back", nil); page["title"] != "/first" {
		t.Fatal(page)
	}
	var blockedVisits atomic.Int32
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { blockedVisits.Add(1) }))
	defer blocked.Close()
	_, _ = d.Call(t.Context(), "navigate", map[string]any{"url": blocked.URL})
	if blockedVisits.Load() != 0 {
		t.Fatal("browser bypassed the managed-port proxy")
	}
	callBrowser(t, d, "navigate", map[string]any{"url": address + "/first"})
	second := callBrowser(t, d, "navigate", map[string]any{"url": address + "/second", "new_tab": true})["active_tab_id"]
	if second == first {
		t.Fatal("new tab reused target")
	}
	if tabs := callBrowser(t, d, "list_tabs", nil)["tabs"].([]map[string]any); len(tabs) != 2 {
		t.Fatal(tabs)
	}
	if page = callBrowser(t, d, "switch_tab", map[string]any{"tab_id": first}); page["title"] != "/first" {
		t.Fatal(page)
	}
	if result := callBrowser(t, d, "close_tab", map[string]any{"tab_id": first}); result["active_tab_id"] != second {
		t.Fatal(result)
	}
	if result := callBrowser(t, d, "close_tab", map[string]any{"tab_id": second}); result["active_tab_id"] != nil {
		t.Fatal(result)
	}
	if result := callBrowser(t, d, "start", nil); result["active_tab_id"] == nil {
		t.Fatal("failed to recover closed target")
	}
	for _, invalid := range []string{"file:///etc/passwd", "http://localhost:9000", "http://169.254.169.254/"} {
		if _, err := d.Call(t.Context(), "navigate", map[string]any{"url": invalid}); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
	if visits.Load() < 2 {
		t.Fatal("fixture not reached through proxy")
	}
	callBrowser(t, d, "stop", nil)
	callBrowser(t, d, "start", nil)
	callBrowser(t, d, "stop", nil)
}
func TestBrowserConfigurationAndQueueCancellation(t *testing.T) {
	for _, proxy := range []string{"", "http://example.com:8767", "http://user@127.0.0.1:8767", "http://127.0.0.1:8767/path"} {
		if _, err := NewDriver(Options{Binary: "/chrome", Profile: "/profile", Downloads: "/downloads", Proxy: proxy}); err == nil {
			t.Fatal(proxy)
		}
	}
	var (
		d   *Driver
		err error
	)
	for _, proxy := range []string{"http://127.0.0.1:8767", "http://browser-proxy:18767"} {
		d, err = NewDriver(Options{Binary: "/chrome", Profile: "/profile", Downloads: "/downloads", Proxy: proxy})
		if err != nil {
			t.Fatal(err)
		}
	}
	d.gate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = d.Call(ctx, "start", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-d.gate
}
