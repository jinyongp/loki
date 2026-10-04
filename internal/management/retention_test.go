package management

import (
	"context"
	"io"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func olderFixture(t *testing.T, s Store, base Installation, digit string, age time.Duration) Installation {
	t.Helper()
	base.Artifact.SHA256 = strings.Repeat(digit, 64)
	dir, err := s.Generation(base.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(dir, ".loki-owner.json"), Owner{Schema: 1, Identity: base.Artifact.Identity()}); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(dir, "module.json"), base.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(dir, ".loki-generation.json"), generationRecord{Schema: 1, PreparedAt: time.Now().UTC().Add(-age), Installation: base}); err != nil {
		t.Fatal(err)
	}
	return base
}

func TestPrunePreservesCurrentLeasedAndUserData(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, _ := s.Load()
	current := ownedFixture(t, s, "browser")
	state.Installed["browser"] = current
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	busy := olderFixture(t, s, current, "b", 3*time.Hour)
	_ = olderFixture(t, s, current, "c", time.Hour)
	removed := olderFixture(t, s, current, "d", 2*time.Hour)
	unlock, err := s.generationLock(busy.Artifact, true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	data := filepath.Join(s.Root, "data", "browser", "profile")
	if err := os.MkdirAll(filepath.Dir(data), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, []byte("user data"), 0600); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(s.Root, "tools", "browser", "generations", "unknown")
	if err := os.MkdirAll(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	report, err := s.Prune(context.Background(), "browser", 1, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Removed) != 1 || len(report.Kept) != 2 || len(report.Busy) != 1 || len(report.Unowned) != 1 {
		t.Fatalf("unexpected retention report: %+v", report)
	}
	dir, _ := s.Generation(removed.Artifact)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("old inactive generation retained")
	}
	if bytes, err := os.ReadFile(data); err != nil || string(bytes) != "user data" {
		t.Fatal("user data was changed")
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("unknown directory was removed")
	}
}

func TestExplicitCompositionGenerationCanBeReacquiredAndPruned(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, _ := s.Load()
	current := ownedFixture(t, s, "browser")
	current.Manifest.Contract = tools.CompositionContract
	state.Config.Contract = tools.CompositionContract
	state.Config.Release = "0.2.4"
	state.Installed["browser"] = current
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	previous := olderFixture(t, s, current, "e", time.Hour)
	directory, err := s.Generation(previous.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generationMetadata(directory); err != nil {
		t.Fatal("v2 generation cannot be reused:", err)
	}
	report, err := s.Prune(t.Context(), "browser", 0, io.Discard)
	if err != nil || len(report.Removed) != 1 || len(report.Unowned) != 0 {
		t.Fatalf("v2 prune: %+v %v", report, err)
	}
}

func TestInterruptedDeletionResumesAfterMarkersWereRemoved(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, _ := s.Load()
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	i := ownedFixture(t, s, "browser")
	dir, _ := s.Generation(i.Artifact)
	r := retirement{Schema: 1, ID: "op-retirement", Phase: tools.Staged, Artifact: i.Artifact}
	trash := filepath.Join(s.Root, "trash", r.ID)
	if err := os.MkdirAll(filepath.Dir(trash), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, trash); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(trash, ".loki-owner.json")); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(s.Root, "retirement.json"), r); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(trash); !os.IsNotExist(err) {
		t.Fatal("partial deletion did not resume")
	}
	journal, err := s.readRetirement()
	if err != nil || journal.Phase != tools.Committed {
		t.Fatalf("cleanup journal: %+v %v", journal, err)
	}
}
