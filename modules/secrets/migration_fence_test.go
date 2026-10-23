package secret

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"loki/internal/state"
)

func TestApplicationMigrationFenceSurvivesOwnerRestartAndRetainsPlatformOwner(t *testing.T) {
	c := managedAccessFixture(t, true)
	snapshot, err := c.backend().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	readSource := func() []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(c.StateDirectory, "store.json"))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	original := readSource()
	const operation = "123e4567-e89b-42d3-a456-426614174401"
	if err := c.BeginApplicationMigration(t.Context(), snapshot.Revision+1, operation); !errors.Is(err, state.ErrConflict) {
		t.Fatal("accepted changed source revision", err)
	}
	if err := c.BeginApplicationMigration(t.Context(), snapshot.Revision, operation); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the owner to prove this is a durable source gate, rather
	// than a process-local lock released by a crashed migration command.
	c = Controller{StateDirectory: c.StateDirectory}
	if err := c.BeginApplicationMigration(t.Context(), snapshot.Revision, operation); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSecret(t.Context(), "web", "TOKEN", "changed", false); !errors.Is(err, ErrApplicationMigration) {
		t.Fatal("application writer crossed migration fence", err)
	}
	if _, err := c.ManagedCredentials().Set(t.Context(), ManagedGitHubAppPrivateKey, "changed"); !errors.Is(err, ErrApplicationMigration) {
		t.Fatal("platform writer changed frozen source", err)
	}
	if !bytes.Equal(original, readSource()) {
		t.Fatal("fencing rewrote original encrypted source")
	}
	if err := c.BeginApplicationMigration(t.Context(), snapshot.Revision, "123e4567-e89b-42d3-a456-426614174403"); !errors.Is(err, ErrApplicationMigration) {
		t.Fatal("replaced original operation", err)
	}
	if err := c.FinishApplicationMigration(t.Context(), snapshot.Revision, operation, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSecret(t.Context(), "web", "TOKEN", "changed", false); !errors.Is(err, ErrApplicationMigration) {
		t.Fatal("committed source reopened app writes", err)
	}
	if _, err := c.ManagedCredentials().Set(t.Context(), ManagedGitHubAppPrivateKey, "rotated-platform"); err != nil {
		t.Fatal("lost narrow platform owner after cutover", err)
	}
	// Platform rotation changes the old store's revision but cannot invalidate
	// an acknowledged cutover or reopen legacy application writes.
	if err := c.FinishApplicationMigration(t.Context(), snapshot.Revision, operation, true); err != nil {
		t.Fatal("lost commit ACK is not replayable", err)
	}
	if err := c.FinishApplicationMigration(t.Context(), snapshot.Revision, operation, false); !errors.Is(err, ErrApplicationMigration) {
		t.Fatal("rollback reopened committed source", err)
	}
}

func TestApplicationMigrationRollbackReleasesOnlyOriginalFence(t *testing.T) {
	c := managedAccessFixture(t, false)
	snapshot, err := c.backend().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const operation = "123e4567-e89b-42d3-a456-426614174411"
	if err := c.BeginApplicationMigration(t.Context(), snapshot.Revision, operation); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishApplicationMigration(t.Context(), snapshot.Revision, "123e4567-e89b-42d3-a456-426614174412", false); !errors.Is(err, ErrApplicationMigration) {
		t.Fatal("released another operation", err)
	}
	if err := c.FinishApplicationMigration(t.Context(), snapshot.Revision, operation, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSecret(t.Context(), "web", "TOKEN", "after-rollback", false); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishApplicationMigration(t.Context(), snapshot.Revision, operation, false); err != nil {
		t.Fatal("lost rollback ACK is not replayable", err)
	}
}

func TestApplicationMigrationMalformedFenceFailsClosed(t *testing.T) {
	c := managedAccessFixture(t, false)
	path := filepath.Join(c.StateDirectory, applicationMigrationFenceFile)
	if err := os.WriteFile(path, []byte(`{"version":1,"operation":"123e4567-e89b-42d3-a456-426614174411","source_revision":1,"state":"committed","unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSecret(t.Context(), "web", "TOKEN", "changed", false); !errors.Is(err, ErrApplicationMigration) {
		t.Fatal("malformed fence allowed writes", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(c.StateDirectory, "master.key"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSecret(t.Context(), "web", "TOKEN", "changed", false); !errors.Is(err, ErrApplicationMigration) {
		t.Fatal("symlink fence allowed writes", err)
	}
}
