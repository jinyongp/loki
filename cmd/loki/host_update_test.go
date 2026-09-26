package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
	"loki/internal/host/releases"
)

type mapHostUpdateFetcher struct {
	assets map[string][]byte
}

func (f mapHostUpdateFetcher) Fetch(_ context.Context, assetURL string, maximum int64) ([]byte, error) {
	raw, ok := f.assets[assetURL]
	if !ok {
		return nil, errors.New("unexpected asset URL: " + assetURL)
	}
	if int64(len(raw)) > maximum {
		return nil, errors.New("fixture exceeds requested maximum")
	}
	return append([]byte(nil), raw...), nil
}

type hostUpdateFixture struct {
	tag         string
	host        releases.SupportedHost
	bootstrap   []byte
	manifestRaw []byte
	indexRaw    []byte
	binary      []byte
	generation  lifecycle.Generation
	assets      map[string][]byte
}

func newHostUpdateFixture(t *testing.T, version string, releasedAt time.Time) hostUpdateFixture {
	t.Helper()
	binary := []byte("#!/bin/sh\necho loki " + version + "\n")
	binarySum := sha256.Sum256(binary)
	host := releases.SupportedHost{
		Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64",
	}
	generation, err := releases.NewGeneration(releases.GenerationSpec{
		Version: version, ReleasedAt: releasedAt.UTC().Truncate(time.Second),
		HostBinaryDigest: "sha256:" + hex.EncodeToString(binarySum[:]),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		Components: []releases.Component{{
			Name: "browser", Digest: "sha256:" + strings.Repeat("c", 64), Optional: true,
		}},
		ConfigSchema: 1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: releases.Compatibility{
			Config: releases.SchemaRange{Min: 1, Max: 1}, Policy: releases.SchemaRange{Min: 1, Max: 1},
			Toolchain: releases.SchemaRange{Min: 1, Max: 1}, State: releases.SchemaRange{Min: 1, Max: 1},
		},
		Rollback: releases.RollbackCoverage{
			StateSnapshot: true, ConfigSnapshot: true, OptionalComponentState: []string{"browser"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := func(path string, marker byte) releases.TargetDescriptor {
		raw := []byte{marker}
		sum := sha256.Sum256(raw)
		return releases.TargetDescriptor{Path: path, Length: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
	}
	manifest, err := releases.NewReleaseManifest(releases.ReleaseManifest{
		Version:    releases.ReleaseManifestVersion,
		Generation: generation,
		HostBinary: releases.TargetDescriptor{
			Path: "releases/bin/loki-" + version + "-linux-amd64", Length: int64(len(binary)),
			SHA256: hex.EncodeToString(binarySum[:]),
		},
		HostAssets:       target("releases/assets/loki-host-"+version+".tar.gz", 'd'),
		ToolchainCatalog: target("toolchains/catalog-"+version+".json", 'e'),
		Provenance:       target("releases/provenance/loki-"+version+".intoto.jsonl", 'f'),
		Notices:          target("releases/notices/loki-"+version+".txt", '1'),
		ReleaseNotes:     target("releases/notes/loki-"+version+".md", '2'),
		SupportedHosts:   []releases.SupportedHost{host},
		Runtime:          releases.RuntimeRequirements{DockerMin: "29.8.1", ComposeMin: "5.5.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := releases.EncodeReleaseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDescriptor := releases.TargetDescriptor{
		Path: "releases/manifests/" + version + ".json", Length: int64(len(manifestRaw)), SHA256: digestBytes(manifestRaw),
	}
	entry, err := releases.IndexEntryForManifest(manifest, manifestDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	index, err := releases.NewReleaseIndex([]releases.ReleaseIndexEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	indexRaw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := lifecycleGenerationFromRelease(generation)
	if err != nil {
		t.Fatal(err)
	}
	tag := "v" + version
	bootstrapRaw := []byte("verified bootstrap " + version)
	installer := []byte(
		"release_tag='" + tag + "'\n" +
			"bootstrap_sha256='" + digestBytes(bootstrapRaw) + "'\n",
	)
	assets := map[string][]byte{
		publicHostUpdateInstallerURL: installer,
	}
	for name, raw := range map[string][]byte{
		"loki-bootstrap-linux-amd64": bootstrapRaw,
		"loki-release-manifest.json": manifestRaw,
		"loki-release-index.json":    indexRaw,
		"loki-release-notes.md":      []byte{'2'},
		"loki-linux-amd64":           binary,
	} {
		assetURL, urlErr := hostUpdateReleaseAssetURL(tag, name)
		if urlErr != nil {
			t.Fatal(urlErr)
		}
		assets[assetURL] = raw
	}
	return hostUpdateFixture{
		tag: tag, host: host, bootstrap: bootstrapRaw, manifestRaw: manifestRaw,
		indexRaw: indexRaw, binary: binary, generation: candidate, assets: assets,
	}
}

func installHostUpdateFixture(t *testing.T, installed lifecycle.Generation) (*lifecycle.FileStore, lifecycle.InstallationState) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	stateRoot := filepath.Join(t.TempDir(), "lifecycle")
	store, err := lifecycle.EnsureFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	installation := lifecycle.InstallationState{
		Scope: "user", Workspace: filepath.Join(t.TempDir(), "workspace"), DockerAccess: "direct",
		MCPPort: lifecycle.DefaultMCPPort,
	}
	if err = os.MkdirAll(installation.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	now := installed.Spec.ReleasedAt.Add(time.Minute)
	if err = store.InitializeInstall(t.Context(), installed, installation, now); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitGeneration(t.Context(), installed, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return store, installation
}

func lifecycleUpdateGeneration(t *testing.T, version string, releasedAt time.Time) lifecycle.Generation {
	t.Helper()
	generation, err := lifecycle.NewGeneration(lifecycle.GenerationSpec{
		Version: version, ReleasedAt: releasedAt.UTC().Truncate(time.Second),
		HostBinaryDigest: "sha256:" + strings.Repeat("a", 64),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		ConfigSchema:     1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: lifecycle.Compatibility{
			Config: lifecycle.SchemaRange{Min: 1, Max: 1}, Policy: lifecycle.SchemaRange{Min: 1, Max: 1},
			Toolchain: lifecycle.SchemaRange{Min: 1, Max: 1}, State: lifecycle.SchemaRange{Min: 1, Max: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func TestPrepareHostUpdateCandidateVerifiesStagesAndPublishesAvailable(t *testing.T) {
	releasedAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	installed := lifecycleUpdateGeneration(t, "1.2.3", releasedAt.Add(-24*time.Hour))
	store, installation := installHostUpdateFixture(t, installed)
	fixture := newHostUpdateFixture(t, "1.3.0", releasedAt)
	inspector := func(_ context.Context, path string) (hostUpdateReleaseBinding, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return hostUpdateReleaseBinding{}, err
		}
		if string(raw) != string(fixture.bootstrap) {
			return hostUpdateReleaseBinding{}, errors.New("unexpected staged bootstrap")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			return hostUpdateReleaseBinding{}, errors.New("bootstrap staging mode is not private")
		}
		return hostUpdateReleaseBinding{
			ReleaseTag: fixture.tag, ReleaseManifestSHA256: digestBytes(fixture.manifestRaw),
			HostBinarySHA256: strings.TrimPrefix(fixture.generation.Spec.HostBinaryDigest, "sha256:"),
		}, nil
	}

	got, err := prepareHostUpdateCandidateWith(
		t.Context(), store, mapHostUpdateFetcher{assets: fixture.assets}, inspector, fixture.host,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != fixture.generation.ID {
		t.Fatalf("candidate = %#v", got)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Available == nil || snapshot.Available.ID != fixture.generation.ID ||
		snapshot.Installed == nil || snapshot.Installed.ID != installed.ID ||
		snapshot.AvailableMetadata == nil || snapshot.AvailableMetadata.GenerationID != fixture.generation.ID ||
		snapshot.AvailableMetadata.DockerMin != "29.8.1" || snapshot.AvailableMetadata.ComposeMin != "5.5.1" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	status, err := (lifecycle.Manager{Store: store}).Status(t.Context())
	if err != nil || status.AvailableMetadata == nil ||
		status.AvailableMetadata.ReleaseNotesPath == "" || status.AvailableMetadata.ReleaseNotesSHA256 == "" ||
		status.AvailableMetadata.ReleaseNotes != "2" {
		t.Fatalf("update status metadata = %#v err=%v", status.AvailableMetadata, err)
	}
	paths, err := resolveHostCLIInstallPaths(installation.Scope == "system", fixture.generation.ID)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(paths.Binary)
	if err != nil || string(staged) != string(fixture.binary) {
		t.Fatalf("staged CLI = %q err=%v", staged, err)
	}
	if _, err = os.Lstat(paths.Link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepare switched managed CLI link: %v", err)
	}
}

func TestPrepareHostUpdateCandidateRejectsBootstrapManifestMismatch(t *testing.T) {
	releasedAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	installed := lifecycleUpdateGeneration(t, "1.2.3", releasedAt.Add(-24*time.Hour))
	store, _ := installHostUpdateFixture(t, installed)
	fixture := newHostUpdateFixture(t, "1.3.0", releasedAt)
	_, err := prepareHostUpdateCandidateWith(
		t.Context(), store, mapHostUpdateFetcher{assets: fixture.assets},
		func(context.Context, string) (hostUpdateReleaseBinding, error) {
			return hostUpdateReleaseBinding{
				ReleaseTag: fixture.tag, ReleaseManifestSHA256: strings.Repeat("0", 64),
				HostBinarySHA256: strings.TrimPrefix(fixture.generation.Spec.HostBinaryDigest, "sha256:"),
			}, nil
		},
		fixture.host,
	)
	if err == nil || !strings.Contains(err.Error(), "manifest digest") {
		t.Fatalf("bootstrap/manifest mismatch error = %v", err)
	}
	snapshot, snapshotErr := store.Snapshot(t.Context())
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if snapshot.Available == nil || snapshot.Available.ID != installed.ID {
		t.Fatalf("failed discovery changed available generation: %#v", snapshot.Available)
	}
}

func TestPrepareHostUpdateCandidateRejectsReleaseRollback(t *testing.T) {
	releasedAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	installed := lifecycleUpdateGeneration(t, "1.3.0", releasedAt)
	store, _ := installHostUpdateFixture(t, installed)
	fixture := newHostUpdateFixture(t, "1.2.9", releasedAt.Add(-time.Hour))
	_, err := prepareHostUpdateCandidateWith(
		t.Context(), store, mapHostUpdateFetcher{assets: fixture.assets},
		func(context.Context, string) (hostUpdateReleaseBinding, error) {
			return hostUpdateReleaseBinding{
				ReleaseTag: fixture.tag, ReleaseManifestSHA256: digestBytes(fixture.manifestRaw),
				HostBinarySHA256: strings.TrimPrefix(fixture.generation.Spec.HostBinaryDigest, "sha256:"),
			}, nil
		},
		fixture.host,
	)
	if err == nil || !strings.Contains(err.Error(), "older than the installed") {
		t.Fatalf("rollback discovery error = %v", err)
	}
}

func TestValidateHostUpdateAdvanceRejectsCurrentRelease(t *testing.T) {
	releasedAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	installed := lifecycleUpdateGeneration(t, "1.3.0", releasedAt)
	if err := validateHostUpdateAdvance(installed, installed); err == nil ||
		!strings.Contains(err.Error(), "no newer release") {
		t.Fatalf("current release prepare error = %v", err)
	}
}

func TestParseHostUpdateInstallerRejectsDuplicateIdentity(t *testing.T) {
	_, _, err := parseHostUpdateInstaller([]byte(
		"release_tag='v1.2.3'\nrelease_tag='v1.2.4'\nbootstrap_sha256='" + strings.Repeat("a", 64) + "'\n",
	))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate installer identity error = %v", err)
	}
}

func TestPrepareHostUpdateCandidateDoesNotPublishAcrossLifecycleLock(t *testing.T) {
	releasedAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	installed := lifecycleUpdateGeneration(t, "1.2.3", releasedAt.Add(-24*time.Hour))
	store, _ := installHostUpdateFixture(t, installed)
	fixture := newHostUpdateFixture(t, "1.3.0", releasedAt)
	lock, err := lifecycle.AcquireOperationLock(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	_, err = prepareHostUpdateCandidateWith(
		t.Context(), store, mapHostUpdateFetcher{assets: fixture.assets},
		func(context.Context, string) (hostUpdateReleaseBinding, error) {
			return hostUpdateReleaseBinding{
				ReleaseTag: fixture.tag, ReleaseManifestSHA256: digestBytes(fixture.manifestRaw),
				HostBinarySHA256: strings.TrimPrefix(fixture.generation.Spec.HostBinaryDigest, "sha256:"),
			}, nil
		},
		fixture.host,
	)
	if err == nil || !strings.Contains(err.Error(), "another host lifecycle operation owns the lock") {
		t.Fatalf("lifecycle lock error = %v", err)
	}
	snapshot, snapshotErr := store.Snapshot(t.Context())
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if snapshot.Available == nil || snapshot.Available.ID != installed.ID {
		t.Fatalf("locked discovery changed available generation: %#v", snapshot.Available)
	}
}
