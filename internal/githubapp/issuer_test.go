package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func issuerFixture(t *testing.T, handler http.HandlerFunc) (*Issuer, *rsa.PrivateKey, *atomic.Int32, func(time.Duration)) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); handler(w, r) }))
	t.Cleanup(server.Close)
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	issuer := &Issuer{
		Config: IssuerConfig{AppID: 123, InstallationID: 456, APIVersion: "2026-03-10", MaxResponseBytes: 4096},
		Client: server.Client(), PrivateKey: func(context.Context) (string, error) { return private, nil },
		Now: func() time.Time { return now }, apiURL: server.URL,
	}
	return issuer, key, &calls, func(delta time.Duration) { now = now.Add(delta) }
}

func TestIssuerJWTExchangeAndCache(t *testing.T) {
	var public *rsa.PublicKey
	issuer, key, calls, advance := issuerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/456/access_tokens" ||
			r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" || r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("invalid request")
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			t.Fatal("invalid JWT")
		}
		payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims struct {
			Iat, Exp int64
			Iss      string
		}
		if json.Unmarshal(payload, &claims) != nil || claims.Iss != "123" || claims.Exp-claims.Iat != 600 {
			t.Fatalf("claims: %s", payload)
		}
		signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(public, crypto.SHA256, sum[:], signature) != nil {
			t.Error("invalid signature")
		}
		expires := time.Unix(claims.Iat+60, 0).Add(time.Hour).UTC().Format(time.RFC3339)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"token":"installation-token","permissions":{"issues":"write"},"expires_at":"`+expires+`"}`)
	})
	public = &key.PublicKey
	first, err := issuer.Token(t.Context())
	if err != nil || first != "installation-token" {
		t.Fatal(first, err)
	}
	if second, err := issuer.Token(t.Context()); err != nil || second != first || calls.Load() != 1 {
		t.Fatal("cache miss", second, err, calls.Load())
	}
	advance(59 * time.Minute)
	if _, err = issuer.Token(t.Context()); err != nil || calls.Load() != 2 {
		t.Fatal("pre-expiry refresh failed", err, calls.Load())
	}
}

func TestIssuerDoesNotRetryMutationOrExposeResponse(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusInternalServerError} {
		issuer, _, calls, _ := issuerFixture(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://example.invalid/")
			w.WriteHeader(status)
			io.WriteString(w, "synthetic-private-response")
		})
		_, err := issuer.Token(t.Context())
		if err == nil || strings.Contains(err.Error(), "synthetic-private-response") || calls.Load() != 1 {
			t.Fatalf("unsafe failure: %v calls=%d", err, calls.Load())
		}
	}
}

func TestIssuerRejectsOversizeAndMalformedResponses(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 4097), `{"token":"","expires_at":"2026-09-15T01:00:00Z"}`, `{"token":"x","expires_at":"2026-09-15T00:00:30Z"}`, `{"token":"x","expires_at":"2026-09-15T01:00:00Z"} trailing`} {
		issuer, _, calls, _ := issuerFixture(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, body)
		})
		if _, err := issuer.Token(t.Context()); err == nil || calls.Load() != 1 {
			t.Fatal("invalid response accepted")
		}
	}
}
