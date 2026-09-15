package service

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/secret"
)

func githubTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestGitHubAppKeyRotationDoesNotDisclosePrivateKey(t *testing.T) {
	controller := secret.Controller{StateDirectory: filepath.Join(t.TempDir(), "vault")}
	if _, err := controller.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	op := GitHubOperations(controller)["github_app_key_set"]
	first, second := githubTestKey(t), githubTestKey(t)
	for i, value := range []string{first, second} {
		raw, _ := json.Marshal(map[string]string{"value": value})
		result, err := op.Handle(t.Context(), raw)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(result)
		if strings.Contains(string(data), value) {
			t.Fatal("result disclosed private key")
		}
		if result.(map[string]any)["rotated"] != (i == 1) {
			t.Fatalf("rotation metadata: %#v", result)
		}
	}
	data, err := filepath.Glob(filepath.Join(controller.StateDirectory, "*"))
	if err != nil || len(data) == 0 {
		t.Fatal("vault missing")
	}
	for _, path := range data {
		content, _ := os.ReadFile(path)
		if strings.Contains(string(content), first) || strings.Contains(string(content), second) {
			t.Fatal("vault exposed private key")
		}
	}
}

func TestGitHubAppKeyRejectsInvalidWithoutReplacing(t *testing.T) {
	controller := secret.Controller{StateDirectory: filepath.Join(t.TempDir(), "vault")}
	controller.Initialize(t.Context())
	op := GitHubOperations(controller)["github_app_key_set"]
	valid := githubTestKey(t)
	raw, _ := json.Marshal(map[string]string{"value": valid})
	if _, err := op.Handle(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]string{"value": "bad-private-sentinel"})
	if _, err := op.Handle(t.Context(), raw); err == nil || strings.Contains(err.Error(), "bad-private-sentinel") {
		t.Fatal("unsafe invalid-key result")
	}
	plan, err := controller.ResolveEnvironment(t.Context(), githubVaultProfile, []string{githubPrivateKey})
	if err != nil || plan.Entries()[0] != githubPrivateKey+"="+valid {
		t.Fatal("valid key was replaced")
	}
}
