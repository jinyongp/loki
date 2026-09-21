package browser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewDriverProxyValidation(t *testing.T) {
	for _, tc := range []struct {
		name, proxy string
		valid       bool
	}{
		{"native", "http://127.0.0.1:18767", true},
		{"compose", "http://browser-proxy:18767", true},
		{"minimum-port", "http://127.0.0.1:1", true},
		{"maximum-port", "http://browser-proxy:65535", true},
		{"empty", "", false},
		{"invalid-escape", "http://%", false},
		{"invalid-path-escape", "http://127.0.0.1:18767/%", false},
		{"invalid-bracket", "http://[127.0.0.1:18767", false},
		{"invalid-port", "http://127.0.0.1:abc", false},
		{"control-character", "http://127.0.0.1:18767\n", false},
		{"https", "https://127.0.0.1:18767", false},
		{"socks", "socks5://127.0.0.1:18767", false},
		{"relative", "//127.0.0.1:18767", false},
		{"opaque", "http:127.0.0.1:18767", false},
		{"unmanaged-host", "http://example.com:18767", false},
		{"host-suffix", "http://browser-proxy.example.com:18767", false},
		{"localhost", "http://localhost:18767", false},
		{"ipv6", "http://[::1]:18767", false},
		{"missing-port", "http://127.0.0.1", false},
		{"empty-port", "http://browser-proxy:", false},
		{"zero-port", "http://127.0.0.1:0", false},
		{"negative-port", "http://127.0.0.1:-1", false},
		{"overflow-port", "http://127.0.0.1:65536", false},
		{"integer-overflow", "http://127.0.0.1:999999999999999999999999", false},
		{"credentials", "http://fixture-user:fixture-private@127.0.0.1:18767", false},
		{"empty-user", "http://@127.0.0.1:18767", false},
		{"path", "http://127.0.0.1:18767/", false},
		{"query", "http://127.0.0.1:18767?fixture=private", false},
		{"empty-query", "http://127.0.0.1:18767?", false},
		{"fragment", "http://127.0.0.1:18767#fixture-private", false},
		{"empty-fragment", "http://127.0.0.1:18767#", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			options := Options{
				Binary: filepath.Join(root, "chromium"), Profile: filepath.Join(root, "profile"),
				Downloads: filepath.Join(root, "downloads"), Proxy: tc.proxy,
			}
			defer func() {
				if recover() != nil {
					t.Error("proxy validation panicked")
				}
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Errorf("constructor changed the filesystem: %v", err)
				}
			}()
			driver, err := NewDriver(options)
			if tc.valid {
				if err != nil || driver == nil {
					t.Fatalf("supported managed endpoint rejected: %v", err)
				}
				if driver.command != nil || driver.client != nil || driver.downloads != nil {
					t.Fatal("constructor started browser resources")
				}
				driver.Close()
				return
			}
			if err == nil || driver != nil {
				t.Fatal("invalid proxy returned a driver")
			}
			if err.Error() != "browser requires the managed HTTP proxy" && err.Error() != "invalid browser proxy port" {
				t.Fatal("proxy error exposed input or parser internals")
			}
		})
	}
}

func FuzzNewDriverProxy(f *testing.F) {
	for _, proxy := range []string{
		"", "http://%", "http://[", "http://127.0.0.1:18767", "http://browser-proxy:18767",
		"http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:18767?",
		"http://127.0.0.1:18767#", "http://fixture:private@127.0.0.1:18767", "\x00\n",
	} {
		f.Add(proxy)
	}
	f.Fuzz(func(t *testing.T, proxy string) {
		// NewDriver must remain side-effect free: these paths do not need to exist,
		// and this test never calls start, navigate or any network operation.
		driver, err := NewDriver(Options{
			Binary: "/fixture/chromium", Profile: "/fixture/profile", Downloads: "/fixture/downloads", Proxy: proxy,
		})
		if err != nil {
			if driver != nil {
				t.Fatal("failed validation returned a driver")
			}
			if err.Error() != "browser requires the managed HTTP proxy" && err.Error() != "invalid browser proxy port" {
				t.Fatal("proxy error exposed input or parser internals")
			}
			return
		}
		if driver == nil || driver.command != nil || driver.client != nil || driver.downloads != nil {
			t.Fatal("invalid constructor state")
		}
		driver.Close()
	})
}
