package githubapp

import (
	"bytes"
	"encoding/json"
	"loki/internal/fault"
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

func TestPartialCredentialsHavePublicRecoveryGuidance(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store.json"), []byte("private-ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	c := Credentials{StateDirectory: dir}
	for _, operation := range []func() error{
		func() error { _, err := c.Initialize(t.Context()); return err },
		func() error { _, err := c.Get(t.Context(), PersonalUserTokens); return err },
		func() error { _, err := c.Set(t.Context(), PersonalUserTokens, `{"token":"fixture"}`); return err },
	} {
		detail := fault.Describe(operation())
		if detail.Code != fault.CodeUnavailable || detail.Retryable || !strings.Contains(detail.Message, "GitHub credentials") || !strings.Contains(detail.NextAction, "loki backups list") || strings.Contains(detail.Message, dir) || strings.Contains(detail.Message, "private-ciphertext") {
			t.Fatalf("unhelpful/private recovery guidance: %+v", detail)
		}
	}
}

func TestResumeOnlyRecordedCredentialInitialization(t *testing.T) {
	source := Credentials{StateDirectory: filepath.Join(t.TempDir(), "source")}
	if _, err := source.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(source.StateDirectory, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source.StateDirectory, "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	intent, err := json.Marshal(struct {
		Schema int             `json:"schema"`
		Key    []byte          `json:"key"`
		Store  json.RawMessage `json:"store"`
	}{1, key, data})
	if err != nil {
		t.Fatal(err)
	}
	target := Credentials{StateDirectory: t.TempDir()}
	if err := os.Chmod(target.StateDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target.StateDirectory, "initialization.json"), intent, 0600); err != nil {
		t.Fatal(err)
	}
	if err := target.ResumeInitialization(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Get(t.Context(), PersonalUserTokens); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(target.StateDirectory, "master.key"))
	if err != nil || !bytes.Equal(key, actual) {
		t.Fatal("resume replaced the original key", err)
	}
	unrecorded := Credentials{StateDirectory: t.TempDir()}
	if err := os.WriteFile(filepath.Join(unrecorded.StateDirectory, "master.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	if err := unrecorded.ResumeInitialization(t.Context()); err != nil {
		t.Fatal("unused unrecorded store blocked startup", err)
	}
	if _, err := os.Stat(filepath.Join(unrecorded.StateDirectory, "store.json")); !os.IsNotExist(err) {
		t.Fatal("resume guessed an unrecorded initial state")
	}
}
