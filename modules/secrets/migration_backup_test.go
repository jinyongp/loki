package secret

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"filippo.io/age"
	"loki/internal/state"
)

func TestMigrationCipherPublicationHasOneWinnerAndOneDurableName(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "source.age")
	type result struct {
		body string
		err  error
	}
	done := make(chan result, 2)
	for _, body := range []string{"synthetic-ciphertext-one", "synthetic-ciphertext-two"} {
		go func() { done <- result{body, publishMigrationCipher(path, []byte(body))} }()
	}
	winner := ""
	for range 2 {
		got := <-done
		if got.err == nil {
			if winner != "" {
				t.Fatal("multiple writers replaced the original recovery archive")
			}
			winner = got.body
		} else if !errors.Is(got.err, os.ErrExist) {
			t.Fatal(got.err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil || winner == "" || string(body) != winner {
		t.Fatal("archive does not contain the original winning publication", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		t.Fatal("archive publication left nonprivate or multiply-linked ciphertext", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".loki-migration-cipher-") {
			t.Fatal("cipher publication left an unacknowledged temporary file")
		}
	}
}

func migrationBackupFixture(t *testing.T) (Controller, uint64, string, *age.X25519Identity, string) {
	t.Helper()
	c := managedAccessFixture(t, true)
	if _, err := c.SetSecret(t.Context(), "web", "TOKEN", "backup-secret-canary\nline two", false); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.backend().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const op = "123e4567-e89b-42d3-a456-426614174421"
	if err := c.BeginApplicationMigration(t.Context(), snapshot.Revision, op); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	return c, snapshot.Revision, op, id, filepath.Join(parent, "source.age")
}

func TestApplicationMigrationEncryptedBackupAndOfflineSourceRecovery(t *testing.T) {
	c, revision, op, id, path := migrationBackupFixture(t)
	read := func(path string) []byte {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	originalKey := read(filepath.Join(c.StateDirectory, "master.key"))
	originalCipher := read(filepath.Join(c.StateDirectory, "store.json"))
	var saved MigrationBackupProof
	persist := func(proof MigrationBackupProof) error {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("published archive before recording original host proof", err)
		}
		saved = proof
		return nil
	}
	proof, err := c.BackupApplicationMigration(t.Context(), revision, op, id.Recipient().String(), path, nil, persist)
	if err != nil || proof != saved || !proof.valid() {
		t.Fatal("backup/proof", err)
	}
	archive := read(path)
	if !bytes.HasPrefix(archive, []byte("age-encryption.org/v1")) || bytes.Contains(archive, originalKey) || bytes.Contains(archive, originalCipher) || bytes.Contains(archive, []byte("backup-secret-canary")) {
		t.Fatal("source or recovery key exposed outside encryption")
	}
	// The original source is byte-for-byte unchanged, including its recovery key.
	if !bytes.Equal(originalKey, read(filepath.Join(c.StateDirectory, "master.key"))) || !bytes.Equal(originalCipher, read(filepath.Join(c.StateDirectory, "store.json"))) {
		t.Fatal("backup modified original source")
	}
	if _, err := c.BackupApplicationMigration(t.Context(), revision, op, id.Recipient().String(), path, nil, persist); err == nil {
		t.Fatal("accepted an existing archive without its saved operation proof")
	}
	replayed, err := c.BackupApplicationMigration(t.Context(), revision, op, id.Recipient().String(), path, &saved, func(MigrationBackupProof) error { t.Fatal("rewrote saved proof"); return nil })
	if err != nil || replayed != proof || !bytes.Equal(archive, read(path)) {
		t.Fatal("lost publication ACK changed original backup", err)
	}
	destination := filepath.Join(filepath.Dir(path), "recovered-source")
	if err := RestoreMigrationSource(t.Context(), path, destination, id, proof); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(originalKey, read(filepath.Join(destination, "master.key"))) || !bytes.Equal(originalCipher, read(filepath.Join(destination, "store.json"))) {
		t.Fatal("recovery altered original key/ciphertext")
	}
	restored := Controller{StateDirectory: destination}
	if err := restored.backend().WithSnapshot(t.Context(), &revision, func(state.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := RestoreMigrationSource(t.Context(), path, destination, id, proof); err == nil {
		t.Fatal("replaced an existing recovery destination")
	}
}

func TestApplicationMigrationBackupPublicationAndRecoveryFailures(t *testing.T) {
	c, revision, op, id, path := migrationBackupFixture(t)
	errCheckpoint := errors.New("synthetic canonical host journal failure")
	var saved MigrationBackupProof
	persist := func(p MigrationBackupProof) error { saved = p; return nil }
	if _, err := c.BackupApplicationMigration(t.Context(), revision, op, id.Recipient().String(), path, nil, func(MigrationBackupProof) error { return errCheckpoint }); !errors.Is(err, errCheckpoint) {
		t.Fatal("ignored failed host checkpoint", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published before durable host checkpoint", err)
	}
	for _, tc := range []struct {
		name string
		rev  uint64
		op   string
	}{{"stale-revision", revision + 1, op}, {"different-operation", revision, "123e4567-e89b-42d3-a456-426614174422"}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.BackupApplicationMigration(t.Context(), tc.rev, tc.op, id.Recipient().String(), path, nil, persist); err == nil {
				t.Fatal("backed up changed source/operation")
			}
		})
	}
	if _, err := c.BackupApplicationMigration(t.Context(), revision, op, id.Recipient().String(), path, nil, persist); err != nil {
		t.Fatal(err)
	}
	wrong, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(filepath.Dir(path), "recovery")
	if err := RestoreMigrationSource(t.Context(), path, destination, wrong, saved); err == nil {
		t.Fatal("wrong identity recovered source")
	}
	badRevision := saved
	badRevision.Revision++
	if err := RestoreMigrationSource(t.Context(), path, destination, id, badRevision); err == nil {
		t.Fatal("mismatching source revision recovered source")
	}
	archive, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Match the damaged ciphertext's outer checksum to exercise age's own EOF
	// authentication, independently of the saved-ciphertext integrity guard.
	archive = archive[:len(archive)-16]
	if err := os.WriteFile(path, archive, 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(archive)
	damagedProof := saved
	damagedProof.Bytes = int64(len(archive))
	damagedProof.CipherSHA256 = hex.EncodeToString(h[:])
	if err := RestoreMigrationSource(t.Context(), path, destination, id, damagedProof); err == nil {
		t.Fatal("truncated authenticated stream recovered source")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed recovery published a destination", err)
	}
	if _, err := c.BackupApplicationMigration(t.Context(), revision, op, id.Recipient().String(), path, &saved, persist); err == nil {
		t.Fatal("replaced or accepted mismatching original ciphertext")
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 1 {
		t.Fatal("recovery left private source staging files", err)
	}
}

func TestApplicationMigrationBackupRequiresPrivateOwnedPaths(t *testing.T) {
	c, revision, op, id, path := migrationBackupFixture(t)
	persist := func(MigrationBackupProof) error { return nil }
	if err := c.FinishApplicationMigration(t.Context(), revision, op, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BackupApplicationMigration(t.Context(), revision, op, id.Recipient().String(), path, nil, persist); !errors.Is(err, ErrApplicationMigration) {
		t.Fatal("backup proceeded without durable source fence", err)
	}
	if err := c.BeginApplicationMigration(t.Context(), revision, op); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(c.StateDirectory, "master.key"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BackupApplicationMigration(t.Context(), revision, op, id.Recipient().String(), path, nil, persist); err == nil {
		t.Fatal("followed archive symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BackupApplicationMigration(t.Context(), revision, op, id.Recipient().String(), path, nil, persist); err == nil {
		t.Fatal("accepted public backup parent")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BackupApplicationMigration(ctx, revision, op, id.Recipient().String(), path, nil, persist); err == nil {
		t.Fatal("published canceled backup")
	}
}
