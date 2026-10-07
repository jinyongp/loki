package mcpapp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/transport/toolproxy"
)

type browserSessionPool struct{ pool *toolproxy.HTTPPool }

func newBrowserSessionPool(fallback *mcp.Server, create func(context.Context) (*mcp.Server, *toolproxy.Group, func(), error)) *browserSessionPool {
	return &browserSessionPool{pool: toolproxy.NewHTTPPool(fallback, create)}
}
func (p *browserSessionPool) server(request *http.Request) *mcp.Server { return p.pool.Server(request) }
func (p *browserSessionPool) handler(next http.Handler) http.Handler   { return p.pool.Handler(next) }
func (p *browserSessionPool) close()                                   { p.pool.Close() }

func browserSessionOptions(options []toolproxy.Options) error {
	for _, option := range options {
		if option.Transport == nil || option.Command != nil {
			return fmt.Errorf("full browser sessions require a reconnectable protected service transport")
		}
	}
	return nil
}
