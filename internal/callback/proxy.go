// Package callback exposes one selected managed API session on loopback.
package callback

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"loki/internal/fault"
)

type Proxy struct {
	mu       sync.Mutex
	resolve  func(string) (int, bool)
	port     int
	session  *string
	listener *net.TCPListener
	closed   bool
	ctx      context.Context
	cancel   context.CancelFunc
	workers  sync.WaitGroup
}

func New(port int, resolve func(string) (int, bool)) (*Proxy, error) {
	if port < 0 || port > 65535 || resolve == nil {
		return nil, fault.Error("local callback proxy port is invalid")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Proxy{resolve: resolve, port: port, ctx: ctx, cancel: cancel}, nil
}
func (p *Proxy) Bind(session string) (map[string]any, error) {
	if _, ok := p.resolve(session); !ok {
		return nil, fault.Error("local callback target must be a running managed API session")
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fault.Error("local callback proxy is closed")
	}
	if p.listener == nil {
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p.port})
		if err != nil {
			port := p.port
			p.mu.Unlock()
			return nil, fault.Error(fmt.Sprintf("local callback port %d is unavailable", port))
		}
		p.listener = listener
		p.port = listener.Addr().(*net.TCPAddr).Port
		p.workers.Go(func() { p.serve(listener) })
	}
	p.session = &session
	p.mu.Unlock()
	return p.Status(), nil
}
func (p *Proxy) Status() map[string]any {
	p.mu.Lock()
	session, listening, port := p.session, p.listener != nil, p.port
	p.mu.Unlock()
	var id, target any
	if session != nil {
		id = *session
		if value, ok := p.resolve(*session); ok {
			target = value
		}
	}
	return map[string]any{"bound": target != nil, "listening": listening, "origin": "http://127.0.0.1:" + strconv.Itoa(port), "session_id": id, "target_port": target}
}
func (p *Proxy) Clear(session *string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if session == nil || (p.session != nil && *session == *p.session) {
		p.session = nil
	}
}
func (p *Proxy) Close() {
	p.mu.Lock()
	p.closed = true
	p.cancel()
	listener := p.listener
	p.listener = nil
	p.session = nil
	p.mu.Unlock()
	if listener != nil {
		listener.Close()
	}
	p.workers.Wait()
}
func (p *Proxy) serve(listener *net.TCPListener) {
	slots := make(chan struct{}, 64)
	for {
		conn, err := listener.AcceptTCP()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
			p.workers.Go(func() { defer func() { <-slots }(); defer conn.Close(); p.handle(conn) })
		default:
			conn.Close()
		}
	}
}
func unavailable(conn net.Conn, status, body string) {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "HTTP/1.1 %s\r\nConnection: close\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\n\r\n%s", status, len(body), body)
}
func (p *Proxy) handle(conn *net.TCPConn) {
	stop := context.AfterFunc(p.ctx, func() { conn.Close() })
	defer stop()
	p.mu.Lock()
	session, port := p.session, p.port
	p.mu.Unlock()
	target, ok := 0, false
	if session != nil {
		target, ok = p.resolve(*session)
	}
	if !ok || target < 1 || target > 65535 || target == port {
		unavailable(conn, "503 Service Unavailable", "local callback target is unavailable")
		return
	}
	upstream, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(p.ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(target)))
	if err != nil {
		unavailable(conn, "502 Bad Gateway", "local callback upstream is unavailable")
		return
	}
	defer upstream.Close()
	stopUpstream := context.AfterFunc(p.ctx, func() { upstream.Close() })
	defer stopUpstream()
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, conn); done <- struct{}{} }()
	go func() { io.Copy(conn, upstream); done <- struct{}{} }()
	<-done
	conn.Close()
	upstream.Close()
	<-done
}
