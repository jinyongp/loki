package secret

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"filippo.io/age"
	"golang.org/x/sys/unix"
	"loki/internal/platform/safeio"
	"loki/internal/state"
)

const migrationSourceLimit = 128 << 20
const migrationArchiveLimit = migrationSourceLimit + (1 << 20)

var errMigrationBackup = errors.New("migration source backup is invalid or unavailable")

// MigrationBackupProof contains ciphertext metadata only. The canonical host
// operation must persist it before archive publication, so a lost acknowledgement
// cannot substitute another operation's backup. It never contains a value hash.
type MigrationBackupProof struct {
	Version      int    `json:"version"`
	Operation    string `json:"operation"`
	Revision     uint64 `json:"source_revision"`
	Recipient    string `json:"recipient"`
	CipherSHA256 string `json:"cipher_sha256"`
	Bytes        int64  `json:"bytes"`
}

type migrationBackupManifest struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	Revision  uint64 `json:"source_revision"`
}

func (p MigrationBackupProof) valid() bool {
	hash, err := hex.DecodeString(p.CipherSHA256)
	r, recipientErr := age.ParseX25519Recipient(p.Recipient)
	return p.Version == 1 && requestIDPattern.MatchString(p.Operation) && p.Revision > 0 &&
		err == nil && len(hash) == sha256.Size && p.CipherSHA256 == hex.EncodeToString(hash) &&
		recipientErr == nil && r.String() == p.Recipient && p.Bytes > 0 && p.Bytes <= migrationArchiveLimit
}

func migrationPrivateRead(path string, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "migration-private-file")
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, errMigrationBackup
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > limit {
		return nil, errMigrationBackup
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		clear(b)
		return nil, errMigrationBackup
	}
	return b, nil
}

// migrationPrivateDestination requires an already owned private canonical parent.
// Callers select this lifecycle path; workspace-provided destinations are forbidden.
func migrationPrivateDestination(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errMigrationBackup
	}
	parent := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return errMigrationBackup
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errMigrationBackup
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return errMigrationBackup
	}
	return nil
}

func verifyMigrationCipher(body []byte, proof MigrationBackupProof) error {
	h := sha256.Sum256(body)
	if !proof.valid() || int64(len(body)) != proof.Bytes || hex.EncodeToString(h[:]) != proof.CipherSHA256 {
		return errMigrationBackup
	}
	return nil
}

// BackupApplicationMigration archives the unchanged original key and encrypted
// source INSIDE age encryption to an operator's X25519 recipient. It requires the
// durable pending source fence and keeps its source writer lock until publication.
// persist must durably save the ciphertext proof in the original host operation.
// On an uncertain response pass that saved proof; no existing archive is replaced.
func (c Controller) BackupApplicationMigration(ctx context.Context, revision uint64, operation, recipient, path string, previous *MigrationBackupProof, persist func(MigrationBackupProof) error) (MigrationBackupProof, error) {
	var proof MigrationBackupProof
	r, err := age.ParseX25519Recipient(recipient)
	if err != nil || r.String() != recipient || revision == 0 || !requestIDPattern.MatchString(operation) || persist == nil {
		return proof, errMigrationBackup
	}
	if err := migrationPrivateDestination(path); err != nil {
		return proof, err
	}
	// An existing source filename can never become the backup destination.
	source, err := filepath.EvalSymlinks(c.StateDirectory)
	if err != nil || filepath.Dir(path) == source {
		return proof, errMigrationBackup
	}
	if previous != nil && (!previous.valid() || previous.Operation != operation || previous.Revision != revision || previous.Recipient != recipient) {
		return proof, errMigrationBackup
	}
	err = c.backend().WithSnapshot(ctx, &revision, func(state.Snapshot) error {
		fence, err := c.readApplicationMigrationFence()
		if err != nil || fence == nil || fence.Operation != operation || fence.Revision != revision || fence.State != "pending" {
			return ErrApplicationMigration
		}
		if body, err := migrationPrivateRead(path, migrationArchiveLimit); err == nil {
			if previous == nil || verifyMigrationCipher(body, *previous) != nil {
				return errMigrationBackup
			}
			proof = *previous
			return ctx.Err()
		} else if !errors.Is(err, os.ErrNotExist) {
			return errMigrationBackup
		}
		key, err := migrationPrivateRead(filepath.Join(source, "master.key"), 32)
		if err != nil || len(key) != 32 {
			clear(key)
			return errMigrationBackup
		}
		defer clear(key)
		body, err := migrationPrivateRead(filepath.Join(source, "store.json"), migrationSourceLimit)
		if err != nil {
			return errMigrationBackup
		}
		defer clear(body)
		var encrypted bytes.Buffer
		w, err := age.Encrypt(&encrypted, r)
		if err != nil {
			return errMigrationBackup
		}
		tw := tar.NewWriter(w)
		manifest, _ := json.Marshal(migrationBackupManifest{1, operation, revision})
		for _, entry := range []struct {
			name string
			body []byte
		}{{"manifest.json", manifest}, {"master.key", key}, {"store.json", body}} {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}); err != nil {
				return errMigrationBackup
			}
			if _, err := tw.Write(entry.body); err != nil {
				return errMigrationBackup
			}
		}
		if tw.Close() != nil || w.Close() != nil || encrypted.Len() > migrationArchiveLimit {
			return errMigrationBackup
		}
		h := sha256.Sum256(encrypted.Bytes())
		proof = MigrationBackupProof{1, operation, revision, recipient, hex.EncodeToString(h[:]), int64(encrypted.Len())}
		if err := persist(proof); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return safeio.PublishPrivate(path, encrypted.Bytes(), false)
	})
	return proof, err
}

// RestoreMigrationSource authenticates an entire archive before publishing a NEW
// private source directory. This is an offline source recovery primitive, never
// an application authority/coordination restore or an automatic selector change.
func RestoreMigrationSource(ctx context.Context, archive, destination string, identity *age.X25519Identity, proof MigrationBackupProof) error {
	if identity == nil || !proof.valid() || identity.Recipient().String() != proof.Recipient {
		return errMigrationBackup
	}
	if err := migrationPrivateDestination(destination); err != nil {
		return err
	}
	if err := migrationPrivateDestination(archive); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errMigrationBackup
	}
	ciphertext, err := migrationPrivateRead(archive, migrationArchiveLimit)
	if err != nil || verifyMigrationCipher(ciphertext, proof) != nil {
		return errMigrationBackup
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return errMigrationBackup
	}
	// Read through the authenticated EOF, even beyond tar's end marker. An
	// apparently complete tar prefix is not evidence of a complete age stream.
	plaintext, err := io.ReadAll(io.LimitReader(r, migrationArchiveLimit+1))
	defer clear(plaintext)
	if err != nil || len(plaintext) > migrationArchiveLimit {
		return errMigrationBackup
	}
	tr := tar.NewReader(bytes.NewReader(plaintext))
	entries := make([][]byte, 0, 3)
	defer func() {
		for _, body := range entries {
			clear(body)
		}
	}()
	for i, name := range []string{"manifest.json", "master.key", "store.json"} {
		h, err := tr.Next()
		limit := int64(migrationSourceLimit)
		if i == 0 {
			limit = 4096
		} else if i == 1 {
			limit = 32
		}
		if err != nil || h.Name != name || h.Typeflag != tar.TypeReg || h.Mode != 0600 || h.Size < 0 || h.Size > limit || len(h.PAXRecords) != 0 || h.Linkname != "" {
			return errMigrationBackup
		}
		body, err := io.ReadAll(tr)
		if err != nil || int64(len(body)) != h.Size {
			clear(body)
			return errMigrationBackup
		}
		entries = append(entries, body)
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) || len(entries[1]) != 32 {
		return errMigrationBackup
	}
	var manifest migrationBackupManifest
	d := json.NewDecoder(bytes.NewReader(entries[0]))
	d.DisallowUnknownFields()
	if d.Decode(&manifest) != nil || manifest != (migrationBackupManifest{1, proof.Operation, proof.Revision}) {
		return errMigrationBackup
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return errMigrationBackup
	}
	parent := filepath.Dir(destination)
	stage, err := os.MkdirTemp(parent, ".loki-source-recovery-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for i, name := range []string{"master.key", "store.json"} {
		if err := safeio.PublishPrivate(filepath.Join(stage, name), entries[i+1], false); err != nil {
			return err
		}
	}
	s := state.Store{Dir: stage, Validate: Validate}
	if err := s.WithSnapshot(ctx, &proof.Revision, func(state.Snapshot) error { return nil }); err != nil {
		return errMigrationBackup
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
