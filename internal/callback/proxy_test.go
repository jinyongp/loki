package callback

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCallbackSelectionAndLifetime(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "first "+r.URL.RequestURI()) }))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "second "+r.URL.RequestURI()) }))
	defer second.Close()
	port := func(url string) int {
		_, value, _ := net.SplitHostPort(strings.TrimPrefix(url, "http://"))
		p, _ := strconv.Atoi(value)
		return p
	}
	var mu sync.Mutex
	targets := map[string]int{"first": port(first.URL), "second": port(second.URL)}
	p, err := New(0, func(session string) (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		value, ok := targets[session]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Status()["listening"] != false {
		t.Fatal(p.Status())
	}
	if _, err := p.Bind("unknown"); err == nil {
		t.Fatal("unknown target bound")
	}
	status, err := p.Bind("first")
	if err != nil {
		t.Fatal(err)
	}
	origin := status["origin"].(string)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	get := func(want int, text string) {
		t.Helper()
		res, err := client.Get(origin + "/callback?code=opaque")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want || string(data) != text {
			t.Fatalf("%d %s %v", res.StatusCode, data, err)
		}
	}
	get(200, "first /callback?code=opaque")
	if _, err := p.Bind("second"); err != nil {
		t.Fatal(err)
	}
	get(200, "second /callback?code=opaque")
	other := "first"
	p.Clear(&other)
	get(200, "second /callback?code=opaque")
	mu.Lock()
	delete(targets, "second")
	mu.Unlock()
	get(503, "local callback target is unavailable")
	p.Clear(nil)
	if p.Status()["session_id"] != nil || p.Status()["bound"] != false {
		t.Fatal(p.Status())
	}
	p.Close()
	if _, err := p.Bind("first"); err == nil {
		t.Fatal("closed proxy rebound")
	}
}

func TestCallbackCloseActiveTunnel(t *testing.T) {
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	target := upstream.Addr().(*net.TCPAddr).Port
	p, err := New(0, func(string) (int, bool) { return target, true })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	status, err := p.Bind("session")
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp4", strings.TrimPrefix(status["origin"].(string), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	accepted := make(chan net.Conn, 1)
	go func() { conn, _ := upstream.Accept(); accepted <- conn }()
	var backend net.Conn
	select {
	case backend = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel not connected")
	}
	defer backend.Close()
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("active tunnel blocked shutdown")
	}
	client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("client survived shutdown")
	}
}
