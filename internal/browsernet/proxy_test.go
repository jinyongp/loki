package browsernet

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loki/internal/platform/netguard"
)

func TestHTTPAndConnectProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy credential forwarded")
		}
		_, _ = io.WriteString(w, r.URL.RequestURI())
	}))
	defer upstream.Close()
	_, portText, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	var allowed atomic.Bool
	allowed.Store(true)
	p := New(netguard.Policy{ValidatePort: func(ctx context.Context, value int) bool { return allowed.Load() && value == port }})
	defer p.Close()
	server := httptest.NewServer(p)
	defer server.Close()
	proxyURL, _ := url.Parse(server.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second * 3}
	request, _ := http.NewRequest("GET", upstream.URL+"/hello?q=1", nil)
	request.Header.Set("Proxy-Authorization", "secret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(data) != "/hello?q=1" {
		t.Fatal(response.StatusCode, string(data), err)
	}
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = fmt.Fprintf(conn, "CONNECT 127.0.0.1:%d HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n", port, port)
	reader := bufio.NewReader(conn)
	response, err = http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != 200 {
		t.Fatal(response, err)
	}
	_, _ = fmt.Fprintf(conn, "GET /tunnel HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: close\r\n\r\n", port)
	response, err = http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(data) != "/tunnel" {
		t.Fatal(string(data), err)
	}
	allowed.Store(false)
	response, err = client.Get(upstream.URL + "/blocked")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	// Validation must run again even when an HTTP connection could be reused.
	if response.StatusCode == 200 {
		t.Fatal("revoked workspace port reused")
	}
}

func TestTunnelIdleTimeoutTracksActivity(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	proxy := New(netguard.Policy{ValidatePort: func(_ context.Context, p int) bool { return p == port }})
	proxy.IdleTimeout = 250 * time.Millisecond
	defer proxy.Close()
	server := httptest.NewServer(proxy)
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "CONNECT 127.0.0.1:%d HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n", port, port)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != 200 {
		t.Fatal(response, err)
	}
	for range 4 {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n", port)
		response, err = http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal("active tunnel expired", err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	if _, err = reader.ReadByte(); err != io.EOF {
		t.Fatal("idle tunnel was not closed", err)
	}
}
