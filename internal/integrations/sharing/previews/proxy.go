package previews

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const MaxRequestBytes = 16 * 1024 * 1024

var privateHeaders = []string{"Cf-Access-Jwt-Assertion", "Cf-Access-Authenticated-User-Email", "Cf-Connecting-Ip", "Cf-Ipcountry", "Cf-Ray", "Cdn-Loop"}

type Proxy struct {
	Store     *Store
	Validate  func(context.Context, Route) bool
	transport *http.Transport
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewProxy(store *Store, validate func(context.Context, Route) bool) *Proxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &Proxy{Store: store, Validate: validate, ctx: ctx, cancel: cancel, transport: &http.Transport{
		DialContext:        (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		DisableCompression: true, MaxIdleConns: 32, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
	}}
}
func (p *Proxy) Close() { p.cancel(); p.transport.CloseIdleConnections() }
func reject(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(message)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, message)
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	r = r.WithContext(ctx)
	preview, ok := p.Store.ResolveHost(r.Host)
	if !ok {
		reject(w, 404, "preview not found")
		return
	}
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	route, path, ok := ResolveRoute(preview, path)
	if !ok {
		reject(w, 404, "preview route not found")
		return
	}
	if p.Validate == nil || !p.Validate(ctx, route) {
		reject(w, 410, "preview server is no longer available")
		return
	}
	if r.Method == "CONNECT" || r.Method == "TRACE" {
		reject(w, 405, "method not allowed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
	if err != nil {
		reject(w, 400, "invalid request body")
		return
	}
	if len(body) > MaxRequestBytes {
		reject(w, 413, "request body is too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	publicHost := strings.ToLower(strings.SplitN(r.Host, ":", 2)[0])
	target := "127.0.0.1:" + strconv.Itoa(route.Port)
	proxy := httputil.ReverseProxy{
		Transport: p.transport, FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = target
			pr.Out.URL.Path = path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = target
			for _, name := range privateHeaders {
				pr.Out.Header.Del(name)
			}
			cookies := []string{}
			for _, line := range pr.Out.Header.Values("Cookie") {
				for _, cookie := range strings.Split(line, ";") {
					cookie = strings.TrimSpace(cookie)
					if cookie != "" && !strings.HasPrefix(strings.ToLower(cookie), "cf_authorization=") {
						cookies = append(cookies, cookie)
					}
				}
			}
			pr.Out.Header.Del("Cookie")
			if len(cookies) > 0 {
				pr.Out.Header.Set("Cookie", strings.Join(cookies, "; "))
			}
			pr.Out.Header.Set("X-Forwarded-Host", publicHost)
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
			pr.Out.Header.Del("X-Forwarded-Prefix")
			if route.Prefix != "/" {
				pr.Out.Header.Set("X-Forwarded-Prefix", route.Prefix)
			}
		},
		ModifyResponse: func(response *http.Response) error {
			h := response.Header
			h.Del("Content-Length")
			response.ContentLength = -1
			h.Set("Cache-Control", "no-store")
			h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
			if location, err := url.Parse(h.Get("Location")); err == nil && (location.Hostname() == "localhost" || location.Hostname() == "127.0.0.1") && location.Port() == strconv.Itoa(route.Port) {
				location.Scheme = "https"
				location.Host = publicHost
				if route.Prefix != "/" {
					location.Path = route.Prefix + location.Path
					location.RawPath = ""
				}
				h.Set("Location", location.String())
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			reject(w, 502, "preview upstream is unavailable")
		},
	}
	proxy.ServeHTTP(w, r)
}
