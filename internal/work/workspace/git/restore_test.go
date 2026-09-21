package gitops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointRepositoryIdentityAndLegacyMetadata(t *testing.T) {
	c := fixture(t)
	c.Config.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	write(t, c, "tracked.txt", "base\n")
	git(t, c, "add", "tracked.txt")
	git(t, c, "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	clone, err := c.git(t.Context(), c.Paths.Root(), nil, 65536, "clone", "--no-hardlinks", "repo", "other")
	if err != nil || clone.ExitCode != 0 {
		t.Fatalf("clone %v %v", clone, err)
	}
	write(t, c, "tracked.txt", "same patch\n")
	os.WriteFile(filepath.Join(c.Paths.Root(), "other", "tracked.txt"), []byte("same patch\n"), 0600)
	first, err := c.Checkpoint(t.Context(), "repo")
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	second, err := c.Checkpoint(t.Context(), "other")
	if err != nil || second == nil || *first == *second {
		t.Fatal("cross-repository checkpoint collision", err)
	}
	metadata, err := c.ReadCheckpoint(*first)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(map[string]any{"repository": metadata.Repository, "created_at": metadata.CreatedAt, "untracked": metadata.Untracked})
	os.WriteFile(filepath.Join(filepath.Dir(c.Config.AuditLog), "checkpoints", *first+".json"), legacy, 0600)
	git(t, c, "restore", "tracked.txt")
	if _, err = c.RestoreCheckpoint(t.Context(), *first); err != nil {
		t.Fatalf("legacy checkpoint restore: %v", err)
	}
}

func TestCheckpointRestoreAndTamperProtection(t *testing.T) {
	c := fixture(t)
	c.Config.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	write(t, c, "tracked.txt", "base\n")
	git(t, c, "add", "tracked.txt")
	git(t, c, "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	write(t, c, "tracked.txt", "restore this\n")
	id, err := c.Checkpoint(t.Context(), "repo")
	if err != nil || id == nil {
		t.Fatal(id, err)
	}
	if _, err = c.RestoreCheckpoint(t.Context(), *id); err == nil {
		t.Fatal("dirty repository restored")
	}
	git(t, c, "restore", "tracked.txt")
	if _, err = c.RestoreCheckpoint(t.Context(), *id); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(c.Paths.Root(), "repo", "tracked.txt"))
	if err != nil || string(data) != "restore this\n" {
		t.Fatal("tracked contents not restored")
	}
	ids, err := c.ListCheckpoints()
	if err != nil || len(ids) != 1 || ids[0] != *id {
		t.Fatal(ids, err)
	}
	git(t, c, "restore", "tracked.txt")
	patch := filepath.Join(filepath.Dir(c.Config.AuditLog), "checkpoints", *id+".patch")
	os.WriteFile(patch, []byte("modified patch"), 0600)
	if _, err = c.RestoreCheckpoint(t.Context(), *id); err == nil {
		t.Fatal("corrupt patch restored")
	}
	if git(t, c, "status", "--porcelain") != "" {
		t.Fatal("failed restore changed worktree")
	}
}
