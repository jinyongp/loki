package auth

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type refreshTransport func(*http.Request) (*http.Response, error)

func (f refreshTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRefreshKeysPreservesLastFile(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		status        int
		valid         bool
	}{
		{"valid", `{"keys":[{"kid":"fixture"}]}`, 200, true}, {"empty", `{"keys":[]}`, 200, false}, {"invalid", "not JSON", 200, false}, {"large", strings.Repeat("x", 1048577), 200, false}, {"failure", `{"keys":[{}]}`, 503, false}, {"redirect", "", 302, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "jwks.json")
			if err := os.WriteFile(target, []byte("old keys"), 0640); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: refreshTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://fixture.cloudflareaccess.com/cdn-cgi/access/certs" || r.Header.Get("Accept") != "application/json" || r.Header.Get("User-Agent") != "loki-jwks-refresh/1" {
					t.Fatal(r.URL, r.Header)
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(test.payload))}, nil
			})}
			err := RefreshKeys(t.Context(), "fixture.cloudflareaccess.com", target, os.Getgid(), client)
			if (err == nil) != test.valid {
				t.Fatal(err)
			}
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			want := "old keys"
			if test.valid {
				want = test.payload
			}
			if string(data) != want {
				t.Fatal("last key file was not preserved")
			}
			if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0640 {
				t.Fatal(info, err)
			}
			entries, _ := os.ReadDir(filepath.Dir(target))
			if len(entries) != 1 {
				t.Fatal("temporary key file retained")
			}
		})
	}
	if RefreshKeys(t.Context(), "cloudflareaccess.com.evil.test", "/unused", 0, nil) == nil {
		t.Fatal("invalid team accepted")
	}
}
