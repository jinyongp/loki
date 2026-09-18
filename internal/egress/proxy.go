// Package egress implements the fixed HTTPS package-download allowlist.
package egress

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"loki/internal/browsernet"
	"loki/internal/platform/netguard"
)

type Proxy struct {
	handler http.Handler
	close   func()
	policy  Policy
	profile string
	audit   func(Decision)
}

type Decision struct {
	Profile string
	Host    string
	Port    int
	Allowed bool
	Reason  string
}

func New(policy Policy, profile string, audit func(Decision)) (*Proxy, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if _, ok := policy.Profiles[profile]; !ok {
		return nil, errors.New("unknown egress profile")
	}
	proxy := browsernet.New(netguard.Policy{})
	proxy.IdleTimeout = 300 * time.Second
	proxy.TunnelErrorStatus = http.StatusBadGateway
	return &Proxy{handler: proxy, close: proxy.Close, policy: policy, profile: profile, audit: audit}, nil
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
		p.record(Decision{Profile: p.profile, Allowed: false, Reason: "request-too-large"})
		response(w, 431)
		return
	}
	host, port, err := net.SplitHostPort(r.RequestURI)
	if err != nil {
		p.record(Decision{Profile: p.profile, Allowed: false, Reason: "invalid-authority"})
		response(w, 400)
		return
	}
	host = strings.ToLower(strings.TrimRight(host, "."))
	number, err := strconv.Atoi(port)
	if err != nil {
		p.record(Decision{Profile: p.profile, Host: host, Allowed: false, Reason: "invalid-port"})
		response(w, 400)
		return
	}
	if r.Method != "CONNECT" || !p.policy.Allows(p.profile, host, number) {
		p.record(Decision{Profile: p.profile, Host: host, Port: number, Allowed: false, Reason: "not-allowlisted"})
		response(w, 403)
		return
	}
	// Only the parsed and allowlisted authority reaches the common tunnel code.
	request := r.Clone(r.Context())
	request.Host = net.JoinHostPort(host, "443")
	request.URL.Host = request.Host
	p.record(Decision{Profile: p.profile, Host: host, Port: number, Allowed: true, Reason: "allowlisted"})
	p.handler.ServeHTTP(w, request)
}

func (p *Proxy) record(decision Decision) {
	if p.audit != nil {
		p.audit(decision)
	}
}
