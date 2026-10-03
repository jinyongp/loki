package githubapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderTokensHaveIndependentEncryptedStorage(t *testing.T) {
	c := Credentials{StateDirectory: filepath.Join(t.TempDir(), "github")}
	value := `{"token":"provider-private-sentinel"}`
	if _, err := c.Set(t.Context(), PersonalUserTokens, value); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(t.Context(), PersonalUserTokens)
	if err != nil || got != value {
		t.Fatalf("provider roundtrip: %v", err)
	}
	if _, err := c.Get(t.Context(), Credential("application-profile")); err == nil {
		t.Fatal("application secret address accepted")
	}
	entries, err := os.ReadDir(c.StateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(c.StateDirectory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "provider-private-sentinel") {
			t.Fatal("provider token persisted as plaintext")
		}
	}
}
