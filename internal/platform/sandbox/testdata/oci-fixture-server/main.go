package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func main() {
	port := flag.Int("port", 0, "development endpoint port")
	allowedAuthority := flag.String("allowed-authority", "", "allowlisted HTTPS CONNECT authority")
	flag.Parse()
	if *port < 1024 || *port > 65535 || *allowedAuthority == "" {
		fatal("invalid fixture arguments")
	}
	proxyRaw := os.Getenv("HTTPS_PROXY")
	if proxyRaw == "" {
		proxyRaw = os.Getenv("https_proxy")
	}
	proxyURL, err := url.Parse(proxyRaw)
	if err != nil || proxyURL.Scheme != "http" || proxyURL.Host == "" || proxyURL.User == nil {
		fatal("authenticated HTTPS proxy environment is missing")
	}

	if status, err := proxyConnectStatus(proxyURL, *allowedAuthority, false); err != nil || status != http.StatusProxyAuthRequired {
		fatal(fmt.Sprintf("unauthenticated proxy access status=%d err=%v", status, err))
	}
	fmt.Println("egress-auth=ok")

	if status, err := proxyConnectStatus(proxyURL, "not-allowlisted.invalid:443", true); err != nil || status != http.StatusForbidden {
		fatal(fmt.Sprintf("disallowed proxy access status=%d err=%v", status, err))
	}
	fmt.Println("egress-denied=ok")

	if status, err := proxyConnectStatus(proxyURL, *allowedAuthority, true); err != nil || status != http.StatusOK {
		fatal(fmt.Sprintf("allowlisted proxy access status=%d err=%v", status, err))
	}
	fmt.Println("egress-allowed=ok")

	if conn, err := net.DialTimeout("tcp", "1.1.1.1:443", 750*time.Millisecond); err == nil {
		_ = conn.Close()
		fatal("direct egress unexpectedly succeeded")
	}
	fmt.Println("egress-direct-denied=ok")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", websocketEcho)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "loki-oci-preview-ok")
	})
	server := &http.Server{
		Addr:              "0.0.0.0:" + strconv.Itoa(*port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Println("ready=1")
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		fatal(err.Error())
	}
}

func proxyConnectStatus(proxyURL *url.URL, authority string, authenticated bool) (int, error) {
	conn, err := net.DialTimeout("tcp", proxyURL.Host, 5*time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", authority, authority); err != nil {
		return 0, err
	}
	if authenticated {
		username := proxyURL.User.Username()
		password, ok := proxyURL.User.Password()
		if username == "" || !ok || password == "" {
			return 0, errors.New("proxy URL lacks basic credentials")
		}
		credential := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		if _, err = fmt.Fprintf(conn, "Proxy-Authorization: Basic %s\r\n", credential); err != nil {
			return 0, err
		}
	}
	if _, err = io.WriteString(conn, "Connection: close\r\n\r\n"); err != nil {
		return 0, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return 0, err
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	return response.StatusCode, nil
}

func websocketEcho(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!headerContains(r.Header.Get("Connection"), "upgrade") ||
		r.Header.Get("Sec-WebSocket-Version") != "13" ||
		r.Header.Get("Sec-WebSocket-Key") == "" {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket unavailable", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + websocketGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	if _, err = fmt.Fprintf(
		rw,
		"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		accept,
	); err != nil {
		return
	}
	if err = rw.Flush(); err != nil {
		return
	}

	var header [2]byte
	if _, err = io.ReadFull(rw, header[:]); err != nil {
		return
	}
	if header[0]&0x0f != 1 || header[0]&0x80 == 0 || header[1]&0x80 == 0 || header[1]&0x7f > 125 {
		return
	}
	length := int(header[1] & 0x7f)
	var mask [4]byte
	if _, err = io.ReadFull(rw, mask[:]); err != nil {
		return
	}
	payload := make([]byte, length)
	if _, err = io.ReadFull(rw, payload); err != nil {
		return
	}
	for index := range payload {
		payload[index] ^= mask[index%len(mask)]
	}
	response := append([]byte{0x81, byte(length)}, payload...)
	_, _ = rw.Write(response)
	_ = rw.Flush()
}

func headerContains(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
