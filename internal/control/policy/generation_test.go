package policy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerationCanonicalDigestAndIsolation(t *testing.T) {
	first, err := NewGeneration(json.RawMessage(`{"z":2,"nested":{"b":2,"a":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewGeneration(json.RawMessage(`{"nested":{"a":1,"b":2},"z":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Valid() || !second.Valid() || first.Digest() != second.Digest() {
		t.Fatalf("equivalent policies produced different generations: %q %q", first.Digest(), second.Digest())
	}
	if metadata := first.Metadata(); metadata.Schema != GenerationSchema || metadata.SHA256 != first.Digest() {
		t.Fatalf("metadata = %#v", metadata)
	}
	if (Generation{}).Valid() || (Generation{}).Metadata() != (GenerationMetadata{}) {
		t.Fatal("zero generation is valid")
	}

	copy := first.CanonicalJSON()
	original := first.CanonicalJSON()
	copy[0] ^= 0xff
	if bytes.Equal(copy, first.CanonicalJSON()) || !bytes.Equal(original, first.CanonicalJSON()) || !first.Valid() {
		t.Fatal("canonical JSON accessor exposed mutable generation state")
	}
}

func TestGenerationDiffIsDeterministicAndBounded(t *testing.T) {
	before, err := NewGeneration(map[string]any{
		"limits":  map[string]any{"max_file_bytes": 10},
		"network": []any{map[string]any{"name": "runtime-default", "mode": "loopback"}},
		"large":   strings.Repeat("a", 1024),
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := NewGeneration(map[string]any{
		"limits":  map[string]any{"max_file_bytes": 11},
		"network": []any{map[string]any{"name": "runtime-default", "mode": "proxy"}},
		"large":   strings.Repeat("b", 1024),
		"feature": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest() == after.Digest() {
		t.Fatal("changed policy retained generation digest")
	}
	changes, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/policy/feature", "/policy/large", "/policy/limits/max_file_bytes", "/policy/network/0/mode"}
	if len(changes) != len(want) {
		t.Fatalf("changes = %#v", changes)
	}
	for i, change := range changes {
		if change.Path != want[i] {
			t.Fatalf("change %d path = %q, want %q", i, change.Path, want[i])
		}
		if len(change.Before) > maxDiffValueBytes || len(change.After) > maxDiffValueBytes {
			t.Fatalf("unbounded diff value: %#v", change)
		}
	}
	if changes[0].Before != "<missing>" || changes[0].After != "true" {
		t.Fatalf("missing-value diff = %#v", changes[0])
	}
	if !strings.Contains(changes[1].Before, `"truncated":true`) || !strings.Contains(changes[1].After, `"sha256"`) {
		t.Fatalf("large diff was not summarized: %#v", changes[1])
	}
	if _, err := Diff(Generation{}, after); err == nil {
		t.Fatal("invalid generation accepted by diff")
	}
}
