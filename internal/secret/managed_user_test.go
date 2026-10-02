package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedGitHubUserTokensStayEncryptedAndHidden(t *testing.T) {
	c := Controller{StateDirectory: filepath.Join(t.TempDir(), "vault")}
	if configured, err := c.ManagedCredentials().Configured(t.Context(), ManagedGitHubUserTokens); err != nil || configured {
		t.Fatal("fresh vault did not report unconfigured", configured, err)
	}
	if _, err := os.Stat(c.StateDirectory); !os.IsNotExist(err) {
		t.Fatal("status initialized the vault", err)
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

func TestManagedUserTokenProvisioningPreservesExistingVault(t *testing.T) {
	c := Controller{StateDirectory: filepath.Join(t.TempDir(), "vault")}
	if _, err := c.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ManagedCredentials().Set(t.Context(), ManagedGitHubAppPrivateKey, "existing-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateProfile(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSecret(t.Context(), "app", "VALUE", "existing-secret", false); err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(filepath.Join(c.StateDirectory, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"first-token", "replacement-token"} {
		if _, err := c.ManagedCredentials().Set(t.Context(), ManagedGitHubUserTokens, value); err != nil {
			t.Fatal(err)
		}
	}
	keyAfter, _ := os.ReadFile(filepath.Join(c.StateDirectory, "master.key"))
	document, err := c.load(t.Context())
	if err != nil || string(keyBefore) != string(keyAfter) {
		t.Fatal("provisioning replaced the existing vault", err)
	}
	app, err := profile(document, "app")
	if err != nil || object(app["secrets"])["VALUE"] != "existing-secret" {
		t.Fatal("provisioning lost application secrets", err)
	}
	if key, err := c.ManagedCredentials().Get(t.Context(), ManagedGitHubAppPrivateKey); err != nil || key != "existing-key" {
		t.Fatal("provisioning lost App private key", err)
	}
}

func TestManagedUserTokenProvisioningRejectsDamagedVault(t *testing.T) {
	for _, name := range []string{"master.key", "store.json", "corrupt"} {
		t.Run(name, func(t *testing.T) {
			c := Controller{StateDirectory: filepath.Join(t.TempDir(), "vault")}
			if _, err := c.Initialize(t.Context()); err != nil {
				t.Fatal(err)
			}
			if name == "corrupt" {
				if err := os.WriteFile(filepath.Join(c.StateDirectory, "store.json"), []byte("damaged"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(filepath.Join(c.StateDirectory, name)); err != nil {
				t.Fatal(err)
			}
			if _, err := c.ManagedCredentials().Set(t.Context(), ManagedGitHubUserTokens, "replacement"); err == nil {
				t.Fatal("damaged vault was silently reinitialized")
			}
		})
	}
}
