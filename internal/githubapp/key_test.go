package githubapp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

func testKey(t *testing.T, bits int) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestValidatePrivateKey(t *testing.T) {
	if err := ValidatePrivateKey(testKey(t, 2048)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "synthetic-private-key", testKey(t, 1024), testKey(t, 2048) + "trailing"} {
		err := ValidatePrivateKey(value)
		if err == nil {
			t.Error("accepted invalid private key")
			continue
		}
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatal("error disclosed private input")
		}
	}
}
