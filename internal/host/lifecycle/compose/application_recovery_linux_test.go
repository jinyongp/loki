//go:build linux

package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"loki/internal/control/appvalues"
)

func TestRecoveryCipherCopyRejectsAliasesAndChangedInput(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.age")
	body := []byte("synthetic ciphertext, not an application value")
	sum := sha256.Sum256(body)
	p := appvalues.ApplicationExportProof{Bytes: len(body), CipherSHA256: hex.EncodeToString(sum[:])}
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyRecoveryCipher(context.Background(), source, filepath.Join(root, "copy.age"), p); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	if err := copyRecoveryCipher(context.Background(), alias, filepath.Join(root, "bad-alias.age"), p); err == nil {
		t.Fatal("recovery followed an input alias")
	}
	body[0] ^= 1
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyRecoveryCipher(context.Background(), source, filepath.Join(root, "changed.age"), p); err == nil {
		t.Fatal("changed ciphertext was published as an authentic recovery artifact")
	}
}

func TestInterruptedRecoveryCopyCleansOnlyItsDeclaredCipherArtifacts(t *testing.T) {
	stage := t.TempDir()
	apps := filepath.Join(stage, "applications")
	if err := os.Mkdir(apps, 0700); err != nil {
		t.Fatal(err)
	}
	proof := appvalues.ApplicationExportProof{Resource: "123e4567-e89b-42d3-a456-426614174441", Bytes: 128}
	if err := os.WriteFile(filepath.Join(apps, restoreCipherName(proof.Resource)), []byte("partial ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(stage, "operator-private-key")
	if err := os.WriteFile(foreign, []byte("synthetic unrelated operator file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeRecoveryStage(stage, []appvalues.ApplicationExportProof{proof}); err == nil {
		t.Fatal("cipher cleanup adopted an undeclared operator file")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("refused cleanup removed unrelated input", err)
	}
	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	if err := removeRecoveryStage(stage, []appvalues.ApplicationExportProof{proof}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stage); !os.IsNotExist(err) {
		t.Fatal("interrupted ciphertext staging remained", err)
	}
}
