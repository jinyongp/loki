// Package auth authenticates the MCP transport and Cloudflare Access assertions.
package auth

import (
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type Verifier interface{ Verify(string) bool }

type Access struct{ TeamDomain, Audience, JWKSPath string }

func (a Access) Verify(assertion string) bool {
	if len(assertion) > 32768 {
		return false
	}
	token, err := jwt.Parse(assertion, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodRS256 {
			return nil, errors.New("unexpected algorithm")
		}
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errors.New("missing key id")
		}
		f, err := os.Open(a.JWKSPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 1_048_577))
		if err != nil || len(data) > 1_048_576 {
			return nil, errors.New("invalid JWKS")
		}
		var doc struct {
			Keys []struct {
				KID string `json:"kid"`
				KTY string `json:"kty"`
				Alg string `json:"alg"`
				Use string `json:"use"`
				N   string `json:"n"`
				E   string `json:"e"`
			} `json:"keys"`
		}
		if err = json.Unmarshal(data, &doc); err != nil {
			return nil, errors.New("invalid JWKS")
		}
		for _, key := range doc.Keys {
			if key.KID != kid {
				continue
			}
			if key.KTY != "RSA" || (key.Alg != "" && key.Alg != "RS256") || (key.Use != "" && key.Use != "sig") {
				return nil, errors.New("invalid signing key")
			}
			n, err := base64.RawURLEncoding.Strict().DecodeString(key.N)
			if err != nil {
				return nil, errors.New("invalid modulus")
			}
			e, err := base64.RawURLEncoding.Strict().DecodeString(key.E)
			if err != nil || len(e) > 4 {
				return nil, errors.New("invalid exponent")
			}
			exponent := new(big.Int).SetBytes(e).Int64()
			if exponent < 3 || exponent > 2147483647 || exponent%2 == 0 {
				return nil, errors.New("invalid exponent")
			}
			modulus := new(big.Int).SetBytes(n)
			if modulus.BitLen() < 2048 {
				return nil, errors.New("invalid modulus")
			}
			return &rsa.PublicKey{N: modulus, E: int(exponent)}, nil
		}
		return nil, errors.New("unknown signing key")
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer("https://"+a.TeamDomain), jwt.WithAudience(a.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	return err == nil && token != nil && token.Valid
}

type Gate struct {
	Token  string
	Access Verifier
}

func (g Gate) Authorized(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) == 1 && subtle.ConstantTimeCompare([]byte(values[0]), []byte("Bearer "+g.Token)) == 1 {
		return true
	}
	assertions := r.Header.Values("Cf-Access-Jwt-Assertion")
	return g.Access != nil && len(assertions) == 1 && g.Access.Verify(assertions[0])
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
// Preview and opaque artifact routes have their own checks before this handler.
func HostPolicy(port int, publicHosts []string, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, host := range publicHosts {
		allowed[host] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		valid := r.Host == "127.0.0.1:"+strconv.Itoa(port) || r.Host == "localhost:"+strconv.Itoa(port)
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
