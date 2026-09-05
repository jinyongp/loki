package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func jwks(t *testing.T, path string, key *rsa.PrivateKey, kid string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"keys": []any{map[string]string{"kid": kid, "kty": "RSA", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAccessSignatureClaimsAndRotation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "jwks.json")
	jwks(t, path, key, "old")
	a := Access{"example.cloudflareaccess.com", strings.Repeat("a", 64), path}
	sign := func(overrides map[string]any, kid string, signingKey any, method jwt.SigningMethod) string {
		claims := jwt.MapClaims{"iss": "https://" + a.TeamDomain, "aud": []string{a.Audience}, "exp": time.Now().Add(time.Minute).Unix()}
		for k, v := range overrides {
			if v == nil {
				delete(claims, k)
			} else {
				claims[k] = v
			}
		}
		token := jwt.NewWithClaims(method, claims)
		token.Header["kid"] = kid
		text, err := token.SignedString(signingKey)
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	valid := sign(nil, "old", key, jwt.SigningMethodRS256)
	if !a.Verify(valid) {
		t.Fatal("valid assertion rejected")
	}
	for _, overrides := range []map[string]any{{"iss": "https://evil.invalid"}, {"aud": "wrong"}, {"exp": time.Now().Add(-time.Second).Unix()}, {"exp": nil}, {"nbf": time.Now().Add(time.Minute).Unix()}, {"iat": time.Now().Add(time.Minute).Unix()}} {
		if a.Verify(sign(overrides, "old", key, jwt.SigningMethodRS256)) {
			t.Fatalf("accepted invalid claims %v", overrides)
		}
	}
	if a.Verify(sign(nil, "old", other, jwt.SigningMethodRS256)) {
		t.Fatal("accepted wrong signature")
	}
	if a.Verify(sign(nil, "old", []byte("synthetic-only"), jwt.SigningMethodHS256)) {
		t.Fatal("accepted algorithm confusion")
	}
	rotated := sign(nil, "new", key, jwt.SigningMethodRS256)
	if a.Verify(rotated) {
		t.Fatal("accepted unknown kid")
	}
	jwks(t, path, key, "new")
	if !a.Verify(rotated) {
		t.Fatal("JWKS rotation not reloaded")
	}
	os.WriteFile(path, []byte("bad json"), 0600)
	if a.Verify(rotated) {
		t.Fatal("accepted corrupt JWKS")
	}
}

type fakeVerifier struct{}

func (fakeVerifier) Verify(value string) bool { return value == "signed-token" }

func TestBearerAndAssertionGate(t *testing.T) {
	gate := Gate{Token: strings.Repeat("t", 48), Access: fakeVerifier{}}
	for _, tc := range []struct {
		bearer, assertion string
		status            int
	}{
		{"Bearer " + gate.Token, "", 204}, {"", "signed-token", 204}, {"", "", 401}, {"Bearer bad", "bad", 401}, {"bearer " + gate.Token, "", 401},
	} {
		r := httptest.NewRequest("POST", "http://localhost/mcp", nil)
		r.Header.Set("Authorization", tc.bearer)
		r.Header.Set("Cf-Access-Jwt-Assertion", tc.assertion)
		w := httptest.NewRecorder()
		gate.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status=%d want %d", w.Code, tc.status)
		}
		if w.Code == 401 && w.Body.String() != `{"error":"unauthorized"}` {
			t.Fatal(w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/mcp", nil)
	r.Header.Add("Authorization", "Bearer "+gate.Token)
	r.Header.Add("Authorization", "Bearer wrong")
	if gate.Authorized(r) {
		t.Fatal("accepted duplicate credentials")
	}
}

func TestHostPolicy(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := HostPolicy(8765, []string{"mcp.example.com"}, next)
	for _, tc := range []struct {
		host, origin string
		status       int
	}{{"127.0.0.1:8765", "", 204}, {"mcp.example.com", "", 204}, {"mcp.example.com:443", "", 204}, {"evil.example", "", 421}, {"mcp.example.com", "https://evil.example", 403}} {
		r := httptest.NewRequest("POST", "http://"+tc.host+"/mcp", nil)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s: %d", tc.host, tc.origin, w.Code)
		}
	}
}
