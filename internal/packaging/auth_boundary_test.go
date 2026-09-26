package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoreAuthDoesNotOwnCloudflareAccessImplementation(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "internal", "auth", "auth.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, forbidden := range []string{
		"Cf-Access-Jwt-Assertion",
		"cloudflareaccess.com",
		"github.com/golang-jwt/jwt",
		"type Access struct",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("core auth owns provider-specific implementation %q", forbidden)
		}
	}
	for _, required := range []string{
		"type RequestVerifier interface",
		"VerifyRequest(*http.Request) bool",
		"External RequestVerifier",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("core auth lacks provider-neutral request verifier contract %q", required)
		}
	}

	adapter, err := os.ReadFile(filepath.Join(root, "internal", "integrations", "access", "cloudflare", "verify.go"))
	if err != nil {
		t.Fatal(err)
	}
	adapterBody := string(adapter)
	for _, required := range []string{
		"Cf-Access-Jwt-Assertion",
		"github.com/golang-jwt/jwt",
		"VerifyRequest(r *http.Request) bool",
	} {
		if !strings.Contains(adapterBody, required) {
			t.Fatalf("Cloudflare adapter lacks %q", required)
		}
	}
	if strings.Contains(adapterBody, "loki/internal/auth") {
		t.Fatal("Cloudflare adapter imports core auth instead of satisfying the request-verifier contract structurally")
	}
}
