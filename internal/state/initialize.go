package state

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const initializationFile = "initialization.json"

// The private intent is durable before either active file is published. It
// carries the original key and encrypted snapshot, so retry never invents a
// replacement key or an empty document for an unexplained partial store.
type initializationIntent struct {
	Schema int             `json:"schema"`
	Key    []byte          `json:"key"`
	Store  json.RawMessage `json:"store"`
}

func (s Store) initialize(ctx context.Context, data json.RawMessage, publish func(string, []byte, bool) error) (bool, error) {
	if err := s.validate(data); err != nil {
		return false, err
	}
	release, err := lock(ctx, s.Dir)
	if err != nil {
		return false, err
	}
	defer release()
	if err = ctx.Err(); err != nil {
		return false, err
	}
	intentPath := filepath.Join(s.Dir, initializationFile)
	if _, err = os.Lstat(intentPath); err == nil {
		return s.resumeInitialization(ctx, publish)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	_, keyErr := os.Lstat(filepath.Join(s.Dir, "master.key"))
	_, storeErr := os.Lstat(filepath.Join(s.Dir, "store.json"))
	if keyErr == nil && storeErr == nil {
		_, _, err = s.load()
		return false, err
	}
	for _, err := range []error{keyErr, storeErr} {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if keyErr == nil || storeErr == nil {
		reason := "missing_data"
		if keyErr != nil {
			reason = "missing_key"
		}
		return false, &RecoveryError{Reason: reason}
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return false, err
	}
	encoded, err := seal(key, Snapshot{Revision: 1, Data: data})
	if err != nil {
		return false, err
	}
	if len(encoded) > maxBytes {
		return false, errors.New("state document exceeds size limit")
	}
	// seal produces compact JSON with one trailing newline. Keep the prepared
	// bytes identical before and after embedding the envelope in the intent.
	intent, err := json.Marshal(initializationIntent{Schema: 1, Key: key, Store: bytes.TrimSpace(encoded)})
	if err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if err = publish(intentPath, intent, false); err != nil {
		return false, err
	}
	return s.resumeInitialization(ctx, publish)
}

// Called under state.lock. Validate every existing file before publishing any
// missing file. Mismatches are corruption, not permission to restore old data.
func (s Store) resumeInitialization(ctx context.Context, publish func(string, []byte, bool) error) (bool, error) {
	intentPath := filepath.Join(s.Dir, initializationFile)
	encoded, err := readPrivate(intentPath, maxBytes+4096)
	if err != nil {
		return false, err
	}
	var intent initializationIntent
	if json.Unmarshal(encoded, &intent) != nil || intent.Schema != 1 || len(intent.Key) != 32 || len(intent.Store) > maxBytes {
		return false, &RecoveryError{Reason: "invalid_record"}
	}
	snapshot, version, err := decrypt(intent.Key, intent.Store)
	if err != nil || version != 2 || snapshot.Revision != 1 {
		return false, &RecoveryError{Reason: "invalid_record"}
	}
	if err = s.validate(snapshot.Data); err != nil {
		return false, err
	}
	files := []struct {
		name    string
		data    []byte
		missing bool
	}{
		{name: "master.key", data: intent.Key},
		{name: "store.json", data: intent.Store},
	}
	for i := range files {
		file := &files[i]
		current, err := readPrivate(filepath.Join(s.Dir, file.name), int64(len(file.data)))
		if errors.Is(err, os.ErrNotExist) {
			file.missing = true
		} else if err != nil {
			return false, err
		} else if !bytes.Equal(current, file.data) {
			return false, &RecoveryError{Reason: "conflicting_record"}
		}
	}
	for _, file := range files {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		if file.missing {
			if err = publish(filepath.Join(s.Dir, file.name), file.data, false); err != nil {
				return false, err
			}
		}
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if err = os.Remove(intentPath); err != nil {
		return false, err
	}
	dir, err := os.Open(s.Dir)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return false, err
	}
	return true, nil
}
