package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedGitHubUserTokensStayEncryptedAndHidden(t *testing.T) {
	c := Controller{StateDirectory: filepath.Join(t.TempDir(), "vault")}
	if _, err := c.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	const value = `{"example-user":{"access_token":"ghu_synthetic","refresh_token":"ghr_synthetic"}}`
	if _, err := c.ManagedCredentials().Set(t.Context(), ManagedGitHubUserTokens, value); err != nil {
		t.Fatal(err)
	}
	if got, err := c.ManagedCredentials().Get(t.Context(), ManagedGitHubUserTokens); err != nil || got != value {
		t.Fatal("managed token did not round trip", err)
	}
	for _, operation := range []func() error{
		func() error { _, err := c.Profile(t.Context(), "github-app"); return err },
		func() error { _, err := c.CreateProfile(t.Context(), "github-app"); return err },
		func() error { _, err := c.RemoveProfile(t.Context(), "github-app"); return err },
		func() error {
			_, err := c.SetSecret(t.Context(), "github-app", "USER_TOKENS", "replacement", false)
			return err
		},
		func() error {
			_, err := c.ImportValues(t.Context(), "github-app", map[string]string{"USER_TOKENS": "replacement"})
			return err
		},
		func() error { _, err := c.ResolveEnvironment(t.Context(), "github-app", nil); return err },
	} {
		if err := operation(); err == nil {
			t.Fatal("application operation accepted managed user credentials")
		}
	}
	metadata, err := c.Profiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range metadata["profiles"].([]map[string]any) {
		if profile["name"] == "github-app" {
			t.Fatal("managed profile exposed through discovery")
		}
	}
	files, err := os.ReadDir(c.StateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(c.StateDirectory, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "ghu_synthetic") || strings.Contains(string(raw), "ghr_synthetic") {
			t.Fatal("managed user token stored as plaintext")
		}
	}
}
