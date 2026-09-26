package lifecycle

import (
	"strings"
	"testing"
	"time"
)

func TestSaveAvailableInvalidatesPreparedPlanWhenCandidateChanges(t *testing.T) {
	store, _, _, candidate, now, _ := transactionFixture(t)
	manager := Manager{Store: store, Now: func() time.Time { return now }}
	if _, err := manager.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := generationFixture(t, "1.2.0", now.Add(-30*time.Minute), 1)
	if next.ID == candidate.ID {
		t.Fatal("fixture did not change generation identity")
	}
	if err := store.SaveAvailable(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Available == nil || snapshot.Available.ID != next.ID || snapshot.Prepared != nil {
		t.Fatalf("available publication did not invalidate stale plan: %#v", snapshot)
	}
}

func TestSaveAvailableMetadataRejectsInconsistentReleaseNotes(t *testing.T) {
	store, _, _, candidate, _, _ := transactionFixture(t)
	metadata := AvailableReleaseMetadata{
		GenerationID: candidate.ID,
		DockerMin:    "29.8.1", ComposeMin: "5.5.1",
		ReleaseNotesPath:   "releases/notes/loki-1.2.0.md",
		ReleaseNotesLength: 1,
		ReleaseNotesSHA256: strings.Repeat("a", 64),
		ReleaseNotes:       "x",
	}
	if err := store.SaveAvailableMetadata(t.Context(), metadata); err == nil {
		t.Fatal("inconsistent available release metadata was accepted")
	}
}
