package egress

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFixedDownloadAllowlist(t *testing.T) {
	proxy := New()
	defer proxy.Close()
	forwarded := ""
	proxy.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = r.Host; w.WriteHeader(200) })
	for host := range allowed {
		for _, authority := range []string{host + ":443", strings.ToUpper(host) + ".:443"} {
			r := httptest.NewRequest("CONNECT", "http://fixture", nil)
			r.RequestURI = authority
			r.Host = "untrusted.example:443"
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, r)
			if w.Code != 200 || forwarded != host+":443" {
				t.Fatal(authority, w.Code, forwarded)
			}
		}
	}
	for _, test := range []struct {
		method, authority string
		status            int
	}{
		{"CONNECT", "registry.npmjs.org:80", 403}, {"CONNECT", "evil.registry.npmjs.org:443", 403}, {"CONNECT", "registry.npmjs.org.evil.test:443", 403},
		{"CONNECT", "127.0.0.1:443", 403}, {"CONNECT", "github.com", 400}, {"CONNECT", "user@github.com:443", 403}, {"GET", "github.com:443", 403},
		{"CONNECT", strings.Repeat("x", 8192) + ":443", 431},
	} {
		forwarded = ""
		r := httptest.NewRequest(test.method, "http://fixture", nil)
		r.RequestURI = test.authority
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		if w.Code != test.status || forwarded != "" {
			t.Fatal(test, w.Code, forwarded)
		}
	}
	r := httptest.NewRequest("CONNECT", "http://fixture", nil)
	r.RequestURI = "github.com:443"
	r.Header.Set("X-Large", strings.Repeat("x", 65536))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, r)
	if w.Code != 431 {
		t.Fatal(w.Code)
	}
}
