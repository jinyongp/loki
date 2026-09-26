package cloudflare

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func writeJWKS(t *testing.T, path string, key *rsa.PrivateKey, kid string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"keys": []any{map[string]string{
		"kid": kid,
		"kty": "RSA",
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAccessSignatureClaimsRotationAndRequestHeader(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "jwks.json")
	writeJWKS(t, path, key, "old")
	a := Access{TeamDomain: "example.cloudflareaccess.com", Audience: strings.Repeat("a", 64), JWKSPath: path}
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
	req := httptest.NewRequest("POST", "http://localhost/mcp", nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", valid)
	if !a.VerifyRequest(req) {
		t.Fatal("valid Cloudflare request assertion rejected")
	}
	req.Header.Add("Cf-Access-Jwt-Assertion", valid)
	if a.VerifyRequest(req) {
		t.Fatal("duplicate Cloudflare assertions accepted")
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
	writeJWKS(t, path, key, "new")
	if !a.Verify(rotated) {
		t.Fatal("JWKS rotation not reloaded")
	}
	if err = os.WriteFile(path, []byte("bad json"), 0600); err != nil {
		t.Fatal(err)
	}
	if a.Verify(rotated) {
		t.Fatal("accepted corrupt JWKS")
	}
}
