package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointPreservesDirtyIndexAndWorktree(t *testing.T) {
	c := fixture(t)
	c.Config.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	write(t, c, "tracked.txt", "base\n")
	git(t, c, "add", "tracked.txt")
	git(t, c, "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	if checkpoint, err := c.Checkpoint(t.Context(), "repo"); err != nil || checkpoint != nil {
		t.Fatal(checkpoint, err)
	}
	write(t, c, "tracked.txt", "staged\n")
	git(t, c, "add", "tracked.txt")
	write(t, c, "tracked.txt", "unstaged\n")
	write(t, c, "untracked.txt", "preserve me")
	before := index(t, c)
	status := git(t, c, "status", "--porcelain=v1", "-z")
	id, err := c.Checkpoint(t.Context(), "repo")
	if err != nil || id == nil {
		t.Fatal(id, err)
	}
	patch, err := os.ReadFile(filepath.Join(filepath.Dir(c.Config.AuditLog), "checkpoints", *id+".patch"))
	if err != nil || !strings.Contains(string(patch), "+unstaged") {
		t.Fatal(string(patch), err)
	}
	metaPath := filepath.Join(filepath.Dir(c.Config.AuditLog), "checkpoints", *id+".json")
	meta, err := os.ReadFile(metaPath)
	if err != nil || !strings.Contains(string(meta), "untracked.txt") {
		t.Fatal(string(meta), err)
	}
	again, err := c.Checkpoint(t.Context(), "repo")
	if err != nil || again == nil || *again != *id {
		t.Fatal(again, err)
	}
	metaAfter, err := os.ReadFile(metaPath)
	if err != nil || string(metaAfter) != string(meta) {
		t.Fatal("checkpoint metadata overwritten", err)
	}
	if index(t, c) != before || git(t, c, "status", "--porcelain=v1", "-z") != status {
		t.Fatal("checkpoint changed source state")
	}
}
