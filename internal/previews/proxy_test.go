package previews

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPProxy(t *testing.T) {
	var received atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.URL.Path != "/hello" || r.URL.RawQuery != "x=1" || r.Header.Get("Cf-Access-Jwt-Assertion") != "" || r.Header.Get("Cookie") != "keep=yes" || r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Forwarded-Prefix") != "/api" || r.Header.Get("X-Forwarded-Host") == "spoof" {
			t.Errorf("request: %s %+v", r.URL, r.Header)
		}
		w.Header().Set("Location", "http://"+r.Host+"/next?q=2#fragment")
		w.Header().Set("Cache-Control", "public")
		w.Header().Add("Set-Cookie", "a=b")
		w.Header().Add("Set-Cookie", "c=d")
		w.WriteHeader(302)
		_, _ = io.Copy(w, r.Body)
	}))
	defer upstream.Close()
	_, portText, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	s := New("preview.test", 0, nil)
	share, err := s.Publish(map[string]int{"/": port, "/api": port}, "", "", 60)
	if err != nil {
		t.Fatal(err)
	}
	var allowed atomic.Bool
	allowed.Store(true)
	p := NewProxy(s, func(context.Context, Route) bool { return allowed.Load() })
	defer p.Close()
	r := httptest.NewRequest("POST", share["url"].(string)+"/api/hello?x=1", strings.NewReader("body"))
	r.Header.Set("Cf-Access-Jwt-Assertion", "secret")
	r.Header.Set("Cookie", "CF_Authorization=secret; keep=yes")
	r.Header.Set("X-Forwarded-Host", "spoof")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 302 || w.Body.String() != "body" || w.Header().Get("Location") != share["url"].(string)+"/api/next?q=2#fragment" || len(w.Header().Values("Set-Cookie")) != 2 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s %+v", w.Code, w.Body.String(), w.Header())
	}
	allowed.Store(false)
	w = httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", share["url"].(string), nil))
	if w.Code != 410 || received.Load() != 1 {
		t.Fatal(w.Code, received.Load())
	}
	allowed.Store(true)
	w = httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", share["url"].(string), strings.NewReader(strings.Repeat("a", MaxRequestBytes+1))))
	if w.Code != 413 || received.Load() != 1 {
		t.Fatal(w.Code, received.Load())
	}
}

func TestWebsocketUpgradeTunnel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/socket" || r.Header.Get("Sec-Websocket-Protocol") != "test" {
			t.Errorf("%s %+v", r.URL, r.Header)
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Protocol: test\r\n\r\n")
		_ = rw.Flush()
		message := make([]byte, 7)
		if _, err = io.ReadFull(rw, message); err != nil {
			t.Error(err)
			return
		}
		// A masked one-byte text frame, then an unmasked response frame.
		if message[0] != 0x81 || message[1] != 0x81 || message[6]^message[2] != 'x' {
			t.Error(message)
		}
		_, _ = conn.Write([]byte{0x81, 1, 'y'})
	}))
	defer upstream.Close()
	_, portText, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	s := New("preview.test", 0, nil)
	share, _ := s.Publish(map[string]int{"/": port, "/api": port}, "", "", 60)
	p := NewProxy(s, func(context.Context, Route) bool { return true })
	defer p.Close()
	server := httptest.NewServer(p)
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "GET /api/socket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Protocol: test\r\n\r\n", strings.TrimPrefix(share["url"].(string), "https://"))
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 101 || response.Header.Get("X-Robots-Tag") == "" {
		t.Fatal(response)
	}
	_, _ = conn.Write([]byte{0x81, 0x81, 1, 2, 3, 4, 'x' ^ 1})
	frame := make([]byte, 3)
	if _, err = io.ReadFull(reader, frame); err != nil {
		t.Fatal(err)
	}
	if string(frame) != string([]byte{0x81, 1, 'y'}) {
		t.Fatal(frame)
	}
}
