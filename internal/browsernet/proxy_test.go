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
	p := New(Policy{ValidatePort: func(ctx context.Context, value int) bool { return allowed.Load() && value == port }})
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
