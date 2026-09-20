package artifacts

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPublishReplayIdentity(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	s := New(Options{
		BaseURL:      "https://example.test/artifacts",
		AllowedHosts: []string{"example.test"},
		MaxItems:     4,
		MaxBytes:     1024,
		Clock:        func() time.Time { return now },
	})
	requestID := "70000000-0000-4000-8000-000000000001"
	fingerprint := strings.Repeat("a", 64)
	first, err := s.PublishReplay(requestID, fingerprint, []byte("image"), "shot.png", "image/png", strings.Repeat("b", 64), 60, "inline")
	if err != nil {
		t.Fatal(err)
	}
	if first["share_id"] == nil || first["filename"] != "shot.png" || first["bytes"] != 5 ||
		first["mime_type"] != "image/png" || first["disposition"] != "inline" {
		t.Fatalf("first publication = %#v", first)
	}
	second, ok, err := s.Replay(requestID, fingerprint)
	if err != nil || !ok || second["share_id"] != first["share_id"] || second["url"] != first["url"] {
		t.Fatalf("replay = %#v ok=%v err=%v", second, ok, err)
	}
	third, err := s.PublishReplay(requestID, fingerprint, []byte("different"), "other.png", "image/png", strings.Repeat("c", 64), 60, "inline")
	if err != nil || third["share_id"] != first["share_id"] || len(s.List()) != 1 {
		t.Fatalf("publish replay = %#v err=%v list=%#v", third, err, s.List())
	}
	if _, _, err = s.Replay(requestID, strings.Repeat("d", 64)); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed fingerprint error = %v", err)
	}
	if revoked := s.Revoke(first["share_id"].(string)); revoked == nil {
		t.Fatal("revoke failed")
	}
	replayedAfterRevoke, ok, err := s.Replay(requestID, fingerprint)
	if err != nil || !ok || replayedAfterRevoke["share_id"] != first["share_id"] {
		t.Fatalf("replay after revoke = %#v ok=%v err=%v", replayedAfterRevoke, ok, err)
	}
	now = now.Add(60 * time.Second)
	if result, ok, err := s.Replay(requestID, fingerprint); err != nil || ok || result != nil {
		t.Fatalf("expired replay = %#v ok=%v err=%v", result, ok, err)
	}
}

func TestPublishReplayRetainsBoundedExtraMetadata(t *testing.T) {
	s := New(Options{BaseURL: "https://example.test/artifacts", MaxItems: 4, MaxBytes: 1024})
	requestID := "70000000-0000-4000-8000-000000000020"
	fingerprint := strings.Repeat("a", 64)
	result, err := s.PublishReplay(
		requestID, fingerprint, []byte("zip"), "bundle.zip", "application/zip",
		strings.Repeat("b", 64), 60, "attachment",
		map[string]any{"kind": "bundle", "file_count": 2, "paths": []string{"a.txt", "b.txt"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result["kind"] != "bundle" || result["file_count"] != 2 {
		t.Fatalf("publication extras = %#v", result)
	}
	replay, ok, err := s.Replay(requestID, fingerprint)
	if err != nil || !ok || replay["kind"] != "bundle" || replay["file_count"] != 2 {
		t.Fatalf("replayed extras = %#v ok=%v err=%v", replay, ok, err)
	}
	before := len(s.List())
	if _, err := s.PublishReplay(
		"70000000-0000-4000-8000-000000000021", strings.Repeat("c", 64),
		[]byte("x"), "x.txt", "text/plain", strings.Repeat("d", 64), 60, "attachment",
		map[string]any{"url": "https://evil.invalid"},
	); err == nil {
		t.Fatal("reserved replay metadata accepted")
	}
	if len(s.List()) != before {
		t.Fatalf("reserved replay metadata published before validation: %#v", s.List())
	}
	if _, err := s.PublishReplay(
		"70000000-0000-4000-8000-000000000022", strings.Repeat("e", 64),
		[]byte("x"), "x.txt", "text/plain", strings.Repeat("f", 64), 60, "attachment",
		map[string]any{"kind": "file"}, map[string]any{"path": "x.txt"},
	); err == nil {
		t.Fatal("multiple replay metadata maps accepted")
	}
}

func TestPublishReplayValidationAndClear(t *testing.T) {
	s := New(Options{})
	fingerprint := strings.Repeat("a", 64)
	if ValidRequestID("not-a-uuid") {
		t.Fatal("invalid request ID accepted")
	}
	if _, _, err := s.Replay("not-a-uuid", fingerprint); err == nil {
		t.Fatal("invalid request ID replay accepted")
	}
	if _, _, err := s.Replay("70000000-0000-4000-8000-000000000001", "bad"); err == nil {
		t.Fatal("invalid fingerprint accepted")
	}
	if _, err := s.PublishReplay(
		"70000000-0000-4000-8000-000000000001", fingerprint,
		[]byte("x"), "x.txt", "text/plain", strings.Repeat("b", 64), 60, "attachment",
	); err != nil {
		t.Fatal(err)
	}
	s.Clear()
	if result, ok, err := s.Replay("70000000-0000-4000-8000-000000000001", fingerprint); err != nil || ok || result != nil {
		t.Fatalf("clear retained replay = %#v ok=%v err=%v", result, ok, err)
	}
}
