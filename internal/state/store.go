// Package state implements the encrypted, transactional runtime data boundary.
// It does not expose decrypted data through MCP; domain controllers select
// public metadata. Validators execute before a transaction is committed.
package state

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/platform/safeio"
)

var (
	ErrDecrypt       = errors.New("secret store cannot be decrypted")
	ErrConflict      = errors.New("state revision changed; inspect current state and retry")
	ErrUninitialized = errors.New("secret store is not initialized; run 'sudo loki secret init'")
)

const maxBytes = 128 << 20

type Envelope struct {
	Version    int    `json:"version"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}
type Snapshot struct {
	Revision uint64          `json:"revision"`
	Data     json.RawMessage `json:"data"`
}
type Validator func(json.RawMessage) error
type Transformer func(json.RawMessage) (json.RawMessage, error)
type Store struct {
	Dir      string
	Validate Validator
}

func validateJSON(data json.RawMessage) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return errors.New("state document must be an object")
	}
	return nil
}

func (s Store) validate(data json.RawMessage) error {
	if err := validateJSON(data); err != nil {
		return err
	}
	if s.Validate != nil {
		return s.Validate(data)
	}
	return nil
}

func lock(ctx context.Context, dir string) (func(), error) {
	return LockFile(ctx, filepath.Join(dir, "state.lock"))
}

// LockFile serializes cooperating runtime instances around a protected file.
func LockFile(ctx context.Context, path string) (func(), error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "state.lock")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("state lock must be a private regular file")
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { unix.Flock(fd, unix.LOCK_UN); f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func readPrivate(path string, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Base(path))
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("state file must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("state file exceeds size limit")
	}
	return data, nil
}

func seal(key []byte, snapshot Snapshot) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	plaintext, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if len(plaintext) > maxBytes*3/4 {
		return nil, errors.New("state document exceeds size limit")
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, []byte("loki-secret-store-v2"))
	encoded, err := json.Marshal(Envelope{2, base64.StdEncoding.EncodeToString(nonce), base64.StdEncoding.EncodeToString(ciphertext)})
	return append(encoded, '\n'), err
}

func decrypt(key, encoded []byte) (Snapshot, int, error) {
	var e Envelope
	if len(key) != 32 || json.Unmarshal(encoded, &e) != nil || (e.Version != 1 && e.Version != 2) {
		return Snapshot{}, 0, ErrDecrypt
	}
	nonce, err := base64.StdEncoding.Strict().DecodeString(e.Nonce)
	if err != nil {
		return Snapshot{}, 0, ErrDecrypt
	}
	ciphertext, err := base64.StdEncoding.Strict().DecodeString(e.Ciphertext)
	if err != nil {
		return Snapshot{}, 0, ErrDecrypt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Snapshot{}, 0, ErrDecrypt
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return Snapshot{}, 0, ErrDecrypt
	}
	aad := fmt.Sprintf("loki-secret-store-v%d", e.Version)
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return Snapshot{}, 0, ErrDecrypt
	}
	if e.Version == 1 {
		if validateJSON(plaintext) != nil {
			return Snapshot{}, 0, ErrDecrypt
		}
		return Snapshot{Revision: 0, Data: plaintext}, 1, nil
	}
	var s Snapshot
	if json.Unmarshal(plaintext, &s) != nil || s.Revision == 0 || validateJSON(s.Data) != nil {
		return Snapshot{}, 0, ErrDecrypt
	}
	return s, 2, nil
}

func (s Store) load() (Snapshot, []byte, error) {
	key, err := readPrivate(filepath.Join(s.Dir, "master.key"), 32)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil, ErrUninitialized
	}
	if err != nil {
		return Snapshot{}, nil, err
	}
	data, err := readPrivate(filepath.Join(s.Dir, "store.json"), maxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil, ErrUninitialized
	}
	if err != nil {
		return Snapshot{}, nil, err
	}
	snapshot, version, err := decrypt(key, data)
	if err != nil {
		return Snapshot{}, nil, err
	}
	if version != 2 {
		return Snapshot{}, nil, errors.New("legacy state requires explicit migration")
	}
	if err = s.validate(snapshot.Data); err != nil {
		return Snapshot{}, nil, err
	}
	return snapshot, key, nil
}

func (s Store) Load(ctx context.Context) (Snapshot, error) {
	release, err := lock(ctx, s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, ErrUninitialized
	}
	if err != nil {
		return Snapshot{}, err
	}
	defer release()
	snapshot, _, err := s.load()
	return snapshot, err
}

func (s Store) Initialize(ctx context.Context, data json.RawMessage) (bool, error) {
	if err := safeDirectory(s.Dir); err != nil {
		return false, err
	}
	return s.initialize(ctx, data)
}

func safeDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("state directory must be private")
	}
	return nil
}

func (s Store) initialize(ctx context.Context, data json.RawMessage) (bool, error) {
	if err := s.validate(data); err != nil {
		return false, err
	}
	release, err := lock(ctx, s.Dir)
	if err != nil {
		return false, err
	}
	defer release()
	keyPath := filepath.Join(s.Dir, "master.key")
	storePath := filepath.Join(s.Dir, "store.json")
	_, keyErr := os.Lstat(keyPath)
	_, storeErr := os.Lstat(storePath)
	if keyErr == nil && storeErr == nil {
		_, _, err := s.load()
		return false, err
	}
	if !errors.Is(keyErr, os.ErrNotExist) || !errors.Is(storeErr, os.ErrNotExist) {
		return false, errors.New("secret store is only partially initialized")
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return false, err
	}
	encoded, err := seal(key, Snapshot{Revision: 1, Data: data})
	if err != nil {
		return false, err
	}
	if err = safeio.PublishPrivate(keyPath, key, false); err != nil {
		return false, err
	}
	if err = safeio.PublishPrivate(storePath, encoded, false); err != nil {
		return false, err
	}
	return true, nil
}

func (s Store) Update(ctx context.Context, expected *uint64, mutate func(json.RawMessage) (json.RawMessage, error)) (Snapshot, error) {
	release, err := lock(ctx, s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, ErrUninitialized
	}
	if err != nil {
		return Snapshot{}, err
	}
	defer release()
	snapshot, key, err := s.load()
	if err != nil {
		return Snapshot{}, err
	}
	if expected != nil && *expected != snapshot.Revision {
		return Snapshot{}, ErrConflict
	}
	data, err := mutate(bytes.Clone(snapshot.Data))
	if err != nil {
		return Snapshot{}, err
	}
	if err = s.validate(data); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Revision == ^uint64(0) {
		return Snapshot{}, errors.New("state revision exhausted")
	}
	snapshot = Snapshot{Revision: snapshot.Revision + 1, Data: data}
	encoded, err := seal(key, snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if err = safeio.PublishPrivate(filepath.Join(s.Dir, "store.json"), encoded, true); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// ImportLegacy creates a distinct candidate directory. It never changes the
// Python source. The source must be stopped or an isolated backup copy.
func ImportLegacy(ctx context.Context, source, destination string, validator Validator) error {
	return ImportLegacyWithTransform(ctx, source, destination, validator, nil)
}

func legacyFingerprint(key, encoded []byte) string {
	hash := sha256.New()
	hash.Write([]byte("loki-legacy-v1\x00"))
	hash.Write(key)
	hash.Write([]byte{0})
	hash.Write(encoded)
	return hex.EncodeToString(hash.Sum(nil))
}

// ImportLegacyWithTransform decrypts an isolated Python v1 copy, optionally
// converts its document, and writes a separately encrypted Go v2 vault.
func ImportLegacyWithTransform(ctx context.Context, source, destination string, validator Validator, transform Transformer) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	if source == destination {
		return errors.New("migration requires a separate destination")
	}
	key, err := readPrivate(filepath.Join(source, "master.key"), 32)
	if err != nil {
		return err
	}
	encoded, err := readPrivate(filepath.Join(source, "store.json"), maxBytes)
	if err != nil {
		return err
	}
	snapshot, version, err := decrypt(key, encoded)
	if err != nil {
		return err
	}
	if version != 1 {
		return errors.New("migration source must be Python version 1")
	}
	if transform != nil {
		snapshot.Data, err = transform(bytes.Clone(snapshot.Data))
		if err != nil {
			return err
		}
	}
	if validator != nil {
		if err = validator(snapshot.Data); err != nil {
			return err
		}
	}
	fingerprint := legacyFingerprint(key, encoded)
	if _, err = os.Lstat(destination); err == nil {
		marker, err := readPrivate(filepath.Join(destination, "migration.json"), 4096)
		if err != nil {
			return errors.New("destination exists without a valid migration marker")
		}
		var m struct {
			SourceSHA256 string `json:"source_sha256"`
		}
		if json.Unmarshal(marker, &m) != nil || m.SourceSHA256 != fingerprint {
			return errors.New("destination belongs to a different migration")
		}
		_, err = (Store{destination, validator}).Load(ctx)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(destination)
	stage, err := os.MkdirTemp(parent, ".loki-migrate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	snapshot.Revision = 1
	v2, err := seal(key, snapshot)
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{"master.key": key, "store.json": v2} {
		if err = safeio.PublishPrivate(filepath.Join(stage, name), data, false); err != nil {
			return err
		}
	}
	if err = os.Mkdir(filepath.Join(stage, "legacy"), 0700); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"master.key": key, "store.json": encoded} {
		if err = safeio.PublishPrivate(filepath.Join(stage, "legacy", name), data, false); err != nil {
			return err
		}
	}
	loaded, err := (Store{stage, validator}).Load(ctx)
	if err != nil {
		return err
	}
	var a, b any
	for _, item := range []struct {
		data  []byte
		value *any
	}{{snapshot.Data, &a}, {loaded.Data, &b}} {
		decoder := json.NewDecoder(bytes.NewReader(item.data))
		decoder.UseNumber()
		if err = decoder.Decode(item.value); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(a, b) {
		return errors.New("migration readback differs")
	}
	marker, _ := json.Marshal(map[string]any{"source_version": 1, "target_version": 2, "source_sha256": fingerprint})
	if err = safeio.PublishPrivate(filepath.Join(stage, "migration.json"), marker, false); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Linux renameat2 guarantees a concurrent migration cannot replace a target.
	if err = unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// ImportLegacyIntoExistingWithTransform imports an isolated Python v1 copy
// into an existing state directory such as a mounted runtime volume. Existing
// non-vault files are preserved. Vault files are published with no-replace
// semantics and the migration marker is published last, so a completed marker
// always describes a fully published migrated vault.
func ImportLegacyIntoExistingWithTransform(
	ctx context.Context,
	source, destination string,
	validator Validator,
	transform Transformer,
) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	if source == destination {
		return errors.New("migration requires a separate destination")
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("migration destination must be a real directory")
	}

	key, err := readPrivate(filepath.Join(source, "master.key"), 32)
	if err != nil {
		return err
	}
	encoded, err := readPrivate(filepath.Join(source, "store.json"), maxBytes)
	if err != nil {
		return err
	}
	snapshot, version, err := decrypt(key, encoded)
	if err != nil {
		return err
	}
	if version != 1 {
		return errors.New("migration source must be Python version 1")
	}
	if transform != nil {
		snapshot.Data, err = transform(bytes.Clone(snapshot.Data))
		if err != nil {
			return err
		}
	}
	if validator != nil {
		if err = validator(snapshot.Data); err != nil {
			return err
		}
	}
	fingerprint := legacyFingerprint(key, encoded)

	markerPath := filepath.Join(destination, "migration.json")
	if _, markerErr := os.Lstat(markerPath); markerErr == nil {
		marker, readErr := readPrivate(markerPath, 4096)
		if readErr != nil {
			return readErr
		}
		var metadata struct {
			SourceSHA256 string `json:"source_sha256"`
		}
		if json.Unmarshal(marker, &metadata) != nil || metadata.SourceSHA256 != fingerprint {
			return errors.New("destination belongs to a different migration")
		}
		if _, loadErr := (Store{destination, validator}).Load(ctx); loadErr != nil {
			return loadErr
		}
		legacyKey, keyErr := readPrivate(filepath.Join(destination, "legacy", "master.key"), 32)
		legacyStore, storeErr := readPrivate(filepath.Join(destination, "legacy", "store.json"), maxBytes)
		if keyErr != nil || storeErr != nil || legacyFingerprint(legacyKey, legacyStore) != fingerprint {
			return errors.New("destination legacy backup differs from migration marker")
		}
		return nil
	} else if !errors.Is(markerErr, os.ErrNotExist) {
		return markerErr
	}
	for _, name := range []string{"master.key", "store.json", "legacy"} {
		if _, entryErr := os.Lstat(filepath.Join(destination, name)); entryErr == nil {
			return errors.New("migration destination already contains vault state")
		} else if !errors.Is(entryErr, os.ErrNotExist) {
			return entryErr
		}
	}

	stage, err := os.MkdirTemp(destination, ".loki-migrate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	snapshot.Revision = 1
	v2, err := seal(key, snapshot)
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{"master.key": key, "store.json": v2} {
		if err = safeio.PublishPrivate(filepath.Join(stage, name), data, false); err != nil {
			return err
		}
	}
	if err = os.Mkdir(filepath.Join(stage, "legacy"), 0700); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"master.key": key, "store.json": encoded} {
		if err = safeio.PublishPrivate(filepath.Join(stage, "legacy", name), data, false); err != nil {
			return err
		}
	}
	loaded, err := (Store{stage, validator}).Load(ctx)
	if err != nil {
		return err
	}
	var want, got any
	for _, item := range []struct {
		data  []byte
		value *any
	}{{snapshot.Data, &want}, {loaded.Data, &got}} {
		decoder := json.NewDecoder(bytes.NewReader(item.data))
		decoder.UseNumber()
		if err = decoder.Decode(item.value); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(want, got) {
		return errors.New("migration readback differs")
	}
	marker, _ := json.Marshal(map[string]any{
		"source_version": 1,
		"target_version": 2,
		"source_sha256": fingerprint,
	})
	if err = safeio.PublishPrivate(filepath.Join(stage, "migration.json"), marker, false); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}

	published := make([]string, 0, 4)
	cleanupPublished := func() {
		for index := len(published) - 1; index >= 0; index-- {
			_ = os.RemoveAll(filepath.Join(destination, published[index]))
		}
	}
	for _, name := range []string{"master.key", "store.json", "legacy", "migration.json"} {
		if err = unix.Renameat2(
			unix.AT_FDCWD, filepath.Join(stage, name),
			unix.AT_FDCWD, filepath.Join(destination, name),
			unix.RENAME_NOREPLACE,
		); err != nil {
			cleanupPublished()
			return err
		}
		published = append(published, name)
	}
	dir, err := os.Open(destination)
	if err != nil {
		cleanupPublished()
		return err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		cleanupPublished()
		return err
	}
	return nil
}

// RestoreLegacy recreates a Python v1 vault from a migration's immutable
// backup. It never changes the migrated vault or its embedded backup.
func RestoreLegacy(ctx context.Context, migration, destination string) error {
	migration, err := filepath.Abs(migration)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	if migration == destination {
		return errors.New("restore requires a separate destination")
	}
	marker, err := readPrivate(filepath.Join(migration, "migration.json"), 4096)
	if err != nil {
		return err
	}
	var metadata struct {
		SourceSHA256 string `json:"source_sha256"`
	}
	if json.Unmarshal(marker, &metadata) != nil || len(metadata.SourceSHA256) != sha256.Size*2 {
		return errors.New("migration marker is invalid")
	}
	legacy := filepath.Join(migration, "legacy")
	key, err := readPrivate(filepath.Join(legacy, "master.key"), 32)
	if err != nil {
		return err
	}
	encoded, err := readPrivate(filepath.Join(legacy, "store.json"), maxBytes)
	if err != nil {
		return err
	}
	if legacyFingerprint(key, encoded) != metadata.SourceSHA256 {
		return errors.New("legacy backup fingerprint differs from migration marker")
	}
	if _, version, decryptErr := decrypt(key, encoded); decryptErr != nil || version != 1 {
		return ErrDecrypt
	}
	if _, err = os.Lstat(destination); err == nil {
		existingKey, keyErr := readPrivate(filepath.Join(destination, "master.key"), 32)
		existingStore, storeErr := readPrivate(filepath.Join(destination, "store.json"), maxBytes)
		if keyErr == nil && storeErr == nil && legacyFingerprint(existingKey, existingStore) == metadata.SourceSHA256 {
			return nil
		}
		return errors.New("restore destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(destination)
	stage, err := os.MkdirTemp(parent, ".loki-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name, data := range map[string][]byte{"master.key": key, "store.json": encoded} {
		if err = safeio.PublishPrivate(filepath.Join(stage, name), data, false); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
