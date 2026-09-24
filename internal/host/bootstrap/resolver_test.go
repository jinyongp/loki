package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"loki/internal/host/releases"
)

type fakeAssetFetcher struct {
	raw     []byte
	err     error
	url     string
	maximum int64
}

func (f *fakeAssetFetcher) Fetch(_ context.Context, assetURL string, maximum int64) ([]byte, error) {
	f.url = assetURL
	f.maximum = maximum
	if f.err != nil {
		return nil, f.err
	}
	return append([]byte(nil), f.raw...), nil
}

func bootstrapDescriptor(path string, raw []byte) releases.TargetDescriptor {
	sum := sha256.Sum256(raw)
	return releases.TargetDescriptor{
		Path: path, Length: int64(len(raw)), SHA256: hex.EncodeToString(sum[:]),
	}
}

func bootstrapGeneration(t *testing.T, version string, releasedAt time.Time, binary []byte) releases.Generation {
	t.Helper()
	sum := sha256.Sum256(binary)
	generation, err := releases.NewGeneration(releases.GenerationSpec{
		Version: version, ReleasedAt: releasedAt,
		HostBinaryDigest: "sha256:" + hex.EncodeToString(sum[:]),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		ConfigSchema:     1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: releases.Compatibility{
			Config:    releases.SchemaRange{Min: 1, Max: 1},
			Policy:    releases.SchemaRange{Min: 1, Max: 1},
			Toolchain: releases.SchemaRange{Min: 1, Max: 1},
			State:     releases.SchemaRange{Min: 1, Max: 1},
		},
		Rollback: releases.RollbackCoverage{StateSnapshot: true, ConfigSnapshot: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func bootstrapManifest(t *testing.T, version string, releasedAt time.Time, binary []byte) (releases.ReleaseManifest, []byte) {
	t.Helper()
	generation := bootstrapGeneration(t, version, releasedAt, binary)
	descriptor := func(targetPath, fill string) releases.TargetDescriptor {
		return releases.TargetDescriptor{Path: targetPath, Length: 1, SHA256: strings.Repeat(fill, 64)}
	}
	manifest := releases.ReleaseManifest{
		Version:          releases.ReleaseManifestVersion,
		Generation:       generation,
		HostBinary:       bootstrapDescriptor("releases/bin/loki-"+version, binary),
		HostAssets:       descriptor("releases/assets/loki-host-"+version+".tar.gz", "d"),
		ToolchainCatalog: descriptor("toolchains/catalogs/"+version+".json", "e"),
		Provenance:       descriptor("releases/provenance/"+version+".bundle.json", "f"),
		Notices:          descriptor("releases/notices/"+version+".tar.gz", "1"),
		ReleaseNotes:     descriptor("releases/notes/"+version+".md", "2"),
		SupportedHosts: []releases.SupportedHost{
			{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
			{Environment: "wsl", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
		},
		Runtime: releases.RuntimeRequirements{DockerMin: "29.8.1", ComposeMin: "5.5.1"},
	}
	raw, err := releases.EncodeReleaseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := releases.LoadReleaseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return normalized, raw
}

func TestResolveUsesReleaseBoundGitHubAsset(t *testing.T) {
	binary := []byte("loki-host-v2")
	manifest, raw := bootstrapManifest(t, "2.0.0", time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), binary)
	host := releases.SupportedHost{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"}
	fetcher := &fakeAssetFetcher{raw: binary}

	candidate, err := Resolve(t.Context(), raw, host, "v2.0.0", fetcher)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Manifest.Generation.ID != manifest.Generation.ID || string(candidate.Binary) != string(binary) || candidate.Host != host {
		t.Fatalf("candidate = %#v", candidate)
	}
	if fetcher.url != "https://github.com/jinyongp/loki/releases/download/v2.0.0/loki-linux-amd64" {
		t.Fatalf("release asset URL = %q", fetcher.url)
	}
	if fetcher.maximum != manifest.HostBinary.Length {
		t.Fatalf("release asset size limit = %d", fetcher.maximum)
	}
}

func TestResolveRejectsReleaseDrift(t *testing.T) {
	binary := []byte("loki-host-v2")
	_, raw := bootstrapManifest(t, "2.0.0", time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), binary)
	host := releases.SupportedHost{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"}

	t.Run("tag", func(t *testing.T) {
		if _, err := Resolve(t.Context(), raw, host, "v1.0.0", &fakeAssetFetcher{raw: binary}); err == nil {
			t.Fatal("mismatched release tag was accepted")
		}
	})
	t.Run("binary", func(t *testing.T) {
		if _, err := Resolve(t.Context(), raw, host, "v2.0.0", &fakeAssetFetcher{raw: []byte("tampered")}); err == nil {
			t.Fatal("tampered host binary was accepted")
		}
	})
	t.Run("unsupported-host", func(t *testing.T) {
		unsupported := host
		unsupported.Arch = "arm64"
		if _, err := Resolve(t.Context(), raw, unsupported, "v2.0.0", &fakeAssetFetcher{raw: binary}); err == nil {
			t.Fatal("unsupported host was accepted")
		}
	})
	t.Run("download", func(t *testing.T) {
		if _, err := Resolve(t.Context(), raw, host, "v2.0.0", &fakeAssetFetcher{err: errors.New("offline")}); err == nil {
			t.Fatal("release download failure was ignored")
		}
	})
}
