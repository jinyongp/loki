package mcpapp

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/transport/toolproxy"
)

// Each stateful HTTP session owns a protected browser connection. The SDK
// consults its server callback more than once per request, including protocol
// negotiation, so creation is memoized in the request context.
type browserRequestKey struct{}
type browserRequest struct {
	once   sync.Once
	server *mcp.Server
}

type browserSessionPool struct {
	mu       sync.Mutex
	closed   bool
	entries  map[*browserRequest]context.CancelFunc
	create   func(context.Context) (*mcp.Server, *toolproxy.Group, func(), error)
	fallback *mcp.Server
	ctx      context.Context
	cancel   context.CancelFunc
	wait     sync.WaitGroup
}

func newBrowserSessionPool(fallback *mcp.Server, create func(context.Context) (*mcp.Server, *toolproxy.Group, func(), error)) *browserSessionPool {
	ctx, cancel := context.WithCancel(context.Background())
	return &browserSessionPool{fallback: fallback, create: create, entries: map[*browserRequest]context.CancelFunc{}, ctx: ctx, cancel: cancel}
}

func (p *browserSessionPool) server(request *http.Request) *mcp.Server {
	entry, _ := request.Context().Value(browserRequestKey{}).(*browserRequest)
	if entry == nil {
		return p.fallback
	}
	entry.once.Do(func() {
		p.mu.Lock()
		if p.closed || len(p.entries) >= 8 {
			p.mu.Unlock()
			return
		}
		ctx, cancel := context.WithCancel(p.ctx)
		p.entries[entry] = cancel
		p.wait.Add(1)
		p.mu.Unlock()
		server, engines, stop, err := p.create(ctx)
		if err != nil {
			cancel()
			p.mu.Lock()
			delete(p.entries, entry)
			p.mu.Unlock()
			p.wait.Done()
			return
		}
		entry.server = server
		go func() {
			defer p.wait.Done()
			defer func() { p.mu.Lock(); delete(p.entries, entry); p.mu.Unlock() }()
			defer engines.Close()
			defer stop()
			defer cancel()
			// A malformed or abandoned initialization must not retain Chrome.
			deadline := time.NewTimer(30 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				for session := range server.Sessions() {
					context.AfterFunc(ctx, func() { _ = session.Close() })
					_ = session.Wait()
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-deadline.C:
					return
				case <-ticker.C:
				}
			}
		}()
	})
	return entry.server
}

func (p *browserSessionPool) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Header.Get("Mcp-Session-Id") == "" {
			r = r.WithContext(context.WithValue(r.Context(), browserRequestKey{}, &browserRequest{}))
		}
		next.ServeHTTP(w, r)
	})
}

func (p *browserSessionPool) close() {
	p.mu.Lock()
	p.closed = true
	p.cancel()
	p.mu.Unlock()
	p.wait.Wait()
}

func browserSessionOptions(options []toolproxy.Options) error {
	for _, option := range options {
		if option.Transport == nil || option.Command != nil {
			return fmt.Errorf("full browser sessions require a reconnectable protected service transport")
		}
	}
	return nil
}
