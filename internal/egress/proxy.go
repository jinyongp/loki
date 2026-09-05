// Package egress implements the fixed HTTPS package-download allowlist.
package egress

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"loki/internal/browsernet"
)

var allowed = map[string]bool{
	"api.github.com": true, "codeload.github.com": true, "github.com": true, "nodejs.org": true,
	"objects.githubusercontent.com": true, "placehold.co": true, "raw.githubusercontent.com": true,
	"registry.npmjs.org": true, "proxy.golang.org": true, "storage.googleapis.com": true, "sum.golang.org": true,
	"index.crates.io": true, "static.crates.io": true, "static.rust-lang.org": true,
}

type Proxy struct {
	handler http.Handler
	close   func()
}

func New() *Proxy {
	proxy := browsernet.New(browsernet.Policy{})
	proxy.IdleTimeout = 300 * time.Second
	proxy.TunnelErrorStatus = http.StatusBadGateway
	return &Proxy{handler: proxy, close: proxy.Close}
}
func (p *Proxy) Close() { p.close() }
func response(w http.ResponseWriter, status int) {
	w.Header().Set("Connection", "close")
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	line := len(r.Method) + len(r.RequestURI) + len(r.Proto) + 4
	total := line + len(r.Host) + 8
	for name, values := range r.Header {
		for _, value := range values {
			total += len(name) + len(value) + 4
		}
	}
	if line > 8192 || total > 65536 {
		response(w, 431)
		return
	}
	host, port, err := net.SplitHostPort(r.RequestURI)
	if err != nil {
		response(w, 400)
		return
	}
	host = strings.ToLower(strings.TrimRight(host, "."))
	number, err := strconv.Atoi(port)
	if err != nil {
		response(w, 400)
		return
	}
	if r.Method != "CONNECT" || number != 443 || !allowed[host] {
		response(w, 403)
		return
	}
	// Only the parsed and allowlisted authority reaches the common tunnel code.
	request := r.Clone(r.Context())
	request.Host = net.JoinHostPort(host, "443")
	request.URL.Host = request.Host
	p.handler.ServeHTTP(w, request)
}
