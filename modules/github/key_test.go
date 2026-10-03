package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
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

func TestLoadPrivateKeyFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "app.pem")
	value := testKey(t, 2048)
	if err := os.WriteFile(path, []byte(value), 0400); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPrivateKeyFile(t.Context(), path)
	if err != nil || loaded != value {
		t.Fatal(err)
	}
	rotated := testKey(t, 2048)
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(rotated), 0400); err != nil {
		t.Fatal(err)
	}
	if loaded, err = LoadPrivateKeyFile(t.Context(), path); err != nil || loaded != rotated {
		t.Fatal("rotated key was not reloaded", err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadPrivateKeyFile(t.Context(), path); err == nil {
		t.Fatal("missing key accepted")
	}
	if err = os.WriteFile(path, []byte(value), 0400); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.pem")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"relative.pem", link} {
		if _, err = LoadPrivateKeyFile(t.Context(), invalid); err == nil {
			t.Errorf("accepted %s", invalid)
		}
	}
	if err = os.Chmod(path, 0620); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadPrivateKeyFile(t.Context(), path); err == nil {
		t.Fatal("accepted writable private key")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = LoadPrivateKeyFile(canceled, path); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("unstable cancellation error: %v", err)
	}
}
