package releases

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthenticatedMetadataAcceptanceFreshnessRollbackAndRotation(t *testing.T) {
	now := time.Now().UTC()
	expires := now.Add(24 * time.Hour)

	t.Run("expired-timestamp", func(t *testing.T) {
		fixture := newRepositoryFixture(t, now)
		options := uniformRepositoryOptions(expires)
		options.timestampExpires = now.Add(-time.Minute)
		client := openFixtureClient(
			t,
			fixture,
			fixture.repositoryWithOptions(t, 1, options),
			privateReleaseStateRoot(t),
		)
		if err := client.Refresh(t.Context()); err == nil {
			t.Fatal("expired authenticated metadata was accepted")
		}
	})

	t.Run("rollback-after-restart", func(t *testing.T) {
		fixture := newRepositoryFixture(t, now)
		stateRoot := privateReleaseStateRoot(t)
		newer := openFixtureClient(t, fixture, fixture.repository(t, 2, expires), stateRoot)
		if err := newer.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		older := openFixtureClient(t, fixture, fixture.repository(t, 1, expires), stateRoot)
		if err := older.Refresh(t.Context()); err == nil {
			t.Fatal("older metadata was accepted after restart")
		}
	})

	t.Run("root-rotation-recovery", func(t *testing.T) {
		fixture := newRepositoryFixture(t, now)
		repo := fixture.repository(t, 1, expires)
		repo.files[fixture.baseURL+"/2.root.json"] = fixture.rotatedRoot(t, now, 2, true)
		stateRoot := privateReleaseStateRoot(t)
		client := openFixtureClient(t, fixture, repo, stateRoot)
		if err := client.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(stateRoot, "metadata", "root.json"))
		if err != nil {
			t.Fatal(err)
		}
		root, err := parseRoot(raw)
		if err != nil {
			t.Fatal(err)
		}
		if root.Signed.Version != 2 {
			t.Fatalf("trusted root version = %d, want 2", root.Signed.Version)
		}
		restarted := openFixtureClient(t, fixture, fixture.repository(t, 1, expires), stateRoot)
		if err = restarted.Refresh(t.Context()); err != nil {
			t.Fatalf("persisted rotated root was not reusable: %v", err)
		}
	})

	t.Run("delegated-key-rotation", func(t *testing.T) {
		fixture := newRepositoryFixture(t, now)
		stateRoot := privateReleaseStateRoot(t)
		initial := openFixtureClient(t, fixture, fixture.repository(t, 1, expires), stateRoot)
		if _, err := initial.ResolveRelease(t.Context(), "loki-1.2.3.json"); err != nil {
			t.Fatal(err)
		}
		if _, err := initial.ResolveToolchain(t.Context(), "catalog-v7.json"); err != nil {
			t.Fatal(err)
		}

		releaseKey := newFixtureKey(t)
		releaseOptions := uniformRepositoryOptions(expires)
		releaseOptions.releaseKey = &releaseKey
		releaseClient := openFixtureClient(t, fixture, fixture.repositoryWithOptions(t, 2, releaseOptions), stateRoot)
		if _, err := releaseClient.ResolveRelease(t.Context(), "loki-1.2.3.json"); err != nil {
			t.Fatalf("release key rotation failed: %v", err)
		}
		if _, err := releaseClient.ResolveToolchain(t.Context(), "catalog-v7.json"); err != nil {
			t.Fatalf("toolchain delegation changed during release rotation: %v", err)
		}

		toolchainKey := newFixtureKey(t)
		toolchainOptions := uniformRepositoryOptions(expires)
		toolchainOptions.releaseKey = &releaseKey
		toolchainOptions.toolchainKey = &toolchainKey
		toolchainClient := openFixtureClient(t, fixture, fixture.repositoryWithOptions(t, 3, toolchainOptions), stateRoot)
		if _, err := toolchainClient.ResolveToolchain(t.Context(), "catalog-v7.json"); err != nil {
			t.Fatalf("toolchain key rotation failed: %v", err)
		}
		if _, err := toolchainClient.ResolveRelease(t.Context(), "loki-1.2.3.json"); err != nil {
			t.Fatalf("release delegation failed after toolchain rotation: %v", err)
		}
	})
}
