package artifacts

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 5, 0, 0, 0, 123456000, time.UTC)
	s := New(Options{BaseURL: "https://example.test/artifacts/", AllowedHosts: []string{"EXAMPLE.TEST"}, MaxItems: 2, MaxBytes: 6, Clock: func() time.Time { return now }})
	data := []byte("abc")
	p, err := s.Publish(data, "한 글/?.txt", "text/plain", "digest", 60, "attachment")
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'z'
	if p["expires_at"] != "2026-09-05T00:01:00.123456+00:00" {
		t.Fatal(p)
	}
	token := p["share_id"].(string)
	for _, tc := range []struct {
		method, path, host string
		code               int
		body               string
	}{
		{"GET", "/artifacts/" + token, "example.test", 200, "abc"},
		{"HEAD", "/artifacts/" + token, "EXAMPLE.TEST", 200, ""},
		{"GET", "/artifacts/" + token, "evil.test", 421, "misdirected request"},
		{"POST", "/artifacts/" + token, "example.test", 405, "method not allowed"},
		{"GET", "/artifacts/../" + token, "example.test", 404, "not found"},
		{"GET", "/artifacts/invalid", "example.test", 404, "not found"},
	} {
		r := httptest.NewRequest(tc.method, "https://"+tc.host+tc.path, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.code || w.Body.String() != tc.body {
			t.Fatalf("%+v: %d %q", tc, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Header())
		}
		if tc.code == 200 {
			if w.Header().Get("Content-Length") != "3" || w.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal(w.Header())
			}
			if !strings.HasSuffix(w.Header().Get("Content-Disposition"), "%ED%95%9C%20%EA%B8%80%2F%3F.txt") {
				t.Fatal(w.Header())
			}
		}
	}
	if _, err = s.Publish([]byte("abcd"), "b", "text/plain", "", 60, "inline"); !errors.Is(err, ErrCapacityFull) {
		t.Fatalf("byte capacity error = %v", err)
	}
	second, err := s.Publish([]byte("def"), "b", "text/plain", "", 61, "inline")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(nil, "c", "text/plain", "", 60, "inline"); !errors.Is(err, ErrCapacityFull) {
		t.Fatalf("item capacity error = %v", err)
	}
	if rows := s.List(); len(rows) != 2 || rows[0]["share_id"] != token {
		t.Fatal(rows)
	}
	now = now.Add(60 * time.Second)
	if s.Revoke(token) != nil || len(s.List()) != 1 {
		t.Fatal("expiry boundary")
	}
	if s.Revoke(second["share_id"].(string)) == nil || len(s.List()) != 0 {
		t.Fatal("revoke")
	}
	if _, err = s.Publish(data, "c", "text/plain", "", 1, "inline"); err != nil {
		t.Fatal(err)
	}
	s.Clear()
	if len(s.List()) != 0 {
		t.Fatal("clear")
	}
}

func TestConcurrentCapacity(t *testing.T) {
	s := New(Options{MaxItems: 16, MaxBytes: 16})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Publish([]byte("a"), "x", "text/plain", "", 60, "inline")
			_ = s.List()
		}()
	}
	wg.Wait()
	if len(s.List()) != 16 {
		t.Fatal(len(s.List()))
	}
}
