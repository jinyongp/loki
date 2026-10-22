package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWithSnapshotFreezesWritersWithoutChangingSource(t *testing.T) {
	s := newStore(t)
	before, err := os.ReadFile(filepath.Join(s.Dir, "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	revision := uint64(1)
	var borrowed []byte
	err = s.WithSnapshot(t.Context(), &revision, func(snapshot Snapshot) error {
		if snapshot.Revision != revision || string(snapshot.Data) != `{"count":0}` {
			t.Fatal("unexpected source snapshot")
		}
		borrowed = snapshot.Data
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := s.Update(ctx, nil, func(json.RawMessage) (json.RawMessage, error) {
				t.Error("writer entered a frozen source")
				return json.RawMessage(`{"count":1}`), nil
			})
			done <- err
		}()
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("competing writer: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(borrowed, make([]byte, len(borrowed))) {
		t.Fatal("borrowed source bytes survived callback ownership")
	}
	after, err := os.ReadFile(filepath.Join(s.Dir, "store.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("snapshot inspection rewrote encrypted source", err)
	}
	called := false
	revision++
	if err := s.WithSnapshot(t.Context(), &revision, func(Snapshot) error { called = true; return nil }); !errors.Is(err, ErrConflict) || called {
		t.Fatal("stale source entered callback", err)
	}
	if _, err := s.Update(t.Context(), nil, func(data json.RawMessage) (json.RawMessage, error) { return data, nil }); err != nil {
		t.Fatal("snapshot callback retained writer lock", err)
	}
}
