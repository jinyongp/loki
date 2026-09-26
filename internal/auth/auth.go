// Package auth owns provider-neutral MCP bearer and request authentication policy.
package auth

import (
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type RequestVerifier interface {
	VerifyRequest(*http.Request) bool
}

type Gate struct {
	Token    string
	External RequestVerifier
}

func (g Gate) Authorized(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) == 1 && subtle.ConstantTimeCompare([]byte(values[0]), []byte("Bearer "+g.Token)) == 1 {
		return true
	}
	return g.External != nil && g.External.VerifyRequest(r)
}

func (g Gate) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.Authorized(r) {
			Unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func Unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	io.WriteString(w, `{"error":"unauthorized"}`)
}

func LoadToken(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if len(data) > 4096 || len(token) < 43 || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("MCP bearer token must contain at least 256 bits of entropy")
	}
	return token, nil
}

// HostPolicy stores the configured host allowlist and rejects any Origin.
// Loopback hosts accept any valid TCP port because a host-side forwarder may
// publish the fixed container listener on an operator-selected local port.
// Preview and opaque artifact routes have their own checks before this handler.
func HostPolicy(port int, publicHosts []string, next http.Handler) http.Handler {
	_ = port
	allowed := map[string]bool{}
	for _, host := range publicHosts {
		allowed[host] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		valid := loopbackHost(r.Host)
		if allowed[r.Host] {
			valid = true
		}
		if host, _, err := net.SplitHostPort(r.Host); err == nil && allowed[host] {
			valid = true
		}
		if !valid {
			http.Error(w, "Invalid Host header", http.StatusMisdirectedRequest)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "Invalid Origin header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(value string) bool {
	host := value
	if splitHost, splitPort, err := net.SplitHostPort(value); err == nil {
		port, portErr := strconv.Atoi(splitPort)
		if portErr != nil || port < 1 || port > 65535 {
			return false
		}
		host = splitHost
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
