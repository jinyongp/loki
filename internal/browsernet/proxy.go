package browsernet

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"
)

type Proxy struct {
	Policy            Policy
	IdleTimeout       time.Duration
	TunnelErrorStatus int
	ctx               context.Context
	cancel            context.CancelFunc
	transport         *http.Transport
	slots             chan struct{}
}

func New(p Policy) *Proxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &Proxy{Policy: p, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 64), transport: &http.Transport{DialContext: p.Dial, DisableCompression: true, DisableKeepAlives: true, ResponseHeaderTimeout: 30 * time.Second}}
}
func (p *Proxy) Close() { p.cancel(); p.transport.CloseIdleConnections() }
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "proxy capacity exceeded", 503)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	r = r.WithContext(ctx)
	if r.Method == "CONNECT" {
		p.tunnel(w, r)
		return
	}
	if r.URL.Scheme != "http" || r.URL.User != nil || r.URL.Host == "" {
		http.Error(w, "invalid proxy target", 400)
		return
	}
	if _, err := ValidateURL(r.URL.String()); err != nil {
		http.Error(w, "destination is blocked", 403)
		return
	}
	proxy := httputil.ReverseProxy{Transport: p.transport, FlushInterval: -1, Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.Host = pr.Out.URL.Host
		pr.Out.Header.Del("Proxy-Authorization")
		pr.Out.Header.Del("Proxy-Connection")
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "upstream connection failed", 502)
	}}
	proxy.ServeHTTP(w, r)
}
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if _, _, err := net.SplitHostPort(target); err != nil {
		if strings.Contains(target, ":") {
			http.Error(w, "invalid CONNECT authority", 400)
			return
		}
		target = net.JoinHostPort(target, "443")
	}
	upstream, err := p.Policy.Dial(r.Context(), "tcp", target)
	if err != nil {
		status := p.TunnelErrorStatus
		if status == 0 {
			status = 403
		}
		http.Error(w, "destination is unavailable or blocked", status)
		return
	}
	defer upstream.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "tunnel unavailable", 500)
		return
	}
	conn, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	stop := context.AfterFunc(r.Context(), func() { conn.Close(); upstream.Close() })
	defer stop()
	if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = buffer.Flush(); err != nil {
		return
	}
	done := make(chan struct{}, 1)
	var clientReader io.Reader = buffer
	var upstreamReader io.Reader = upstream
	if p.IdleTimeout > 0 {
		touch := func() {
			deadline := time.Now().Add(p.IdleTimeout)
			conn.SetDeadline(deadline)
			upstream.SetDeadline(deadline)
		}
		clientReader = activityReader{buffer, touch}
		upstreamReader = activityReader{upstream, touch}
	}
	go func() {
		_, _ = io.Copy(upstream, clientReader)
		if tcp, ok := upstream.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	_, _ = io.Copy(conn, upstreamReader)
	conn.Close()
	upstream.Close()
	<-done
}

type activityReader struct {
	io.Reader
	touch func()
}

func (r activityReader) Read(data []byte) (int, error) { r.touch(); return r.Reader.Read(data) }
