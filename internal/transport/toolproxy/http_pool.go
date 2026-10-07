package toolproxy

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type httpRequestKey struct{}
type httpRequest struct {
	once   sync.Once
	server *mcp.Server
}

// HTTPPool owns one upstream connection per stateful HTTP session. The SDK
// consults Server repeatedly, so creation is memoized in each initial request.
// Abandoned initialization and idle session closure release upstream processes.
type HTTPPool struct {
	mu       sync.Mutex
	closed   bool
	entries  map[*httpRequest]context.CancelFunc
	create   func(context.Context) (*mcp.Server, *Group, func(), error)
	fallback *mcp.Server
	ctx      context.Context
	cancel   context.CancelFunc
	wait     sync.WaitGroup
}

func NewHTTPPool(fallback *mcp.Server, create func(context.Context) (*mcp.Server, *Group, func(), error)) *HTTPPool {
	ctx, cancel := context.WithCancel(context.Background())
	return &HTTPPool{fallback: fallback, create: create, ctx: ctx, cancel: cancel, entries: map[*httpRequest]context.CancelFunc{}}
}

func (p *HTTPPool) Server(request *http.Request) *mcp.Server {
	entry, _ := request.Context().Value(httpRequestKey{}).(*httpRequest)
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
		server, group, stop, err := p.create(ctx)
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
			defer group.Close()
			defer stop()
			defer cancel()
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

func (p *HTTPPool) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var entry *httpRequest
		if r.Method == http.MethodPost && r.Header.Get("Mcp-Session-Id") == "" {
			entry = &httpRequest{}
			r = r.WithContext(context.WithValue(r.Context(), httpRequestKey{}, entry))
		}
		next.ServeHTTP(w, r)
		if entry != nil && entry.server != nil {
			for range entry.server.Sessions() {
				return
			}
			p.mu.Lock()
			cancel := p.entries[entry]
			p.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		}
	})
}

func (p *HTTPPool) Close() { p.mu.Lock(); p.closed = true; p.cancel(); p.mu.Unlock(); p.wait.Wait() }
