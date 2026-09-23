package releases

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func releaseTarget(targetPath, character string) TargetDescriptor {
	return TargetDescriptor{
		Path:   targetPath,
		Length: 123,
		SHA256: strings.Repeat(character, 64),
	}
}

func releaseManifestDescriptor(t *testing.T, manifest ReleaseManifest) TargetDescriptor {
	t.Helper()
	raw, err := EncodeReleaseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return TargetDescriptor{
		Path:   "releases/manifests/" + manifest.Generation.Spec.Version + ".json",
		Length: int64(len(raw)),
		SHA256: hex.EncodeToString(sum[:]),
	}
}

func releaseManifestFixture(t *testing.T, version string, releasedAt time.Time) ReleaseManifest {
	t.Helper()
	generation := releaseGenerationFixture(t, version, releasedAt, 2)
	return ReleaseManifest{
		Version:          ReleaseManifestVersion,
		Generation:       generation,
		HostBinary:       releaseTarget("releases/bin/loki-"+version, "a"),
		HostAssets:       releaseTarget("releases/assets/loki-host-"+version+".tar.gz", "f"),
		ToolchainCatalog: releaseTarget("toolchains/catalogs/"+version+".json", "1"),
		Provenance:       releaseTarget("releases/provenance/"+version+".bundle.json", "2"),
		Notices:          releaseTarget("releases/notices/"+version+".tar.gz", "3"),
		ReleaseNotes:     releaseTarget("releases/notes/"+version+".md", "4"),
		SupportedHosts: []SupportedHost{
			{Environment: "wsl", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
			{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
		},
		Runtime: RuntimeRequirements{
			DockerMin:  "28.0.0",
			ComposeMin: "2.39.0",
		},
	}
}

func TestReleaseManifestBindsImmutableGenerationAndTargets(t *testing.T) {
	now := time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
	manifest := releaseManifestFixture(t, "1.2.3", now)
	normalized, err := NewReleaseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !normalized.Generation.Valid() || normalized.Generation.ID != manifest.Generation.ID {
		t.Fatalf("generation = %#v", normalized.Generation)
	}
	if normalized.SupportedHosts[0].Environment != "native" || normalized.SupportedHosts[1].Environment != "wsl" {
		t.Fatalf("supported hosts are not canonical: %#v", normalized.SupportedHosts)
	}

	raw, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"host_binary\":{\"path\":") ||
		!strings.Contains(string(raw), "\"generation\":{\"id\":\"sha256:") {
		t.Fatalf("manifest JSON contract is not canonical: %s", raw)
	}
	loaded, err := LoadReleaseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation.ID != normalized.Generation.ID || loaded.HostBinary != normalized.HostBinary {
		t.Fatalf("loaded manifest changed identity: %#v", loaded)
	}
}

func TestReleaseManifestRejectsContractAndNamespaceDrift(t *testing.T) {
	now := time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
	tests := map[string]func(*ReleaseManifest){
		"generation-id-mismatch": func(manifest *ReleaseManifest) {
			manifest.Generation.ID = releaseDigest("9")
		},
		"host-binary-generation-mismatch": func(manifest *ReleaseManifest) {
			manifest.HostBinary.SHA256 = strings.Repeat("9", 64)
		},
		"release-target-outside-role": func(manifest *ReleaseManifest) {
			manifest.ReleaseNotes.Path = "toolchains/notes/release.md"
		},
		"toolchain-target-outside-role": func(manifest *ReleaseManifest) {
			manifest.ToolchainCatalog.Path = "releases/toolchains/catalog.json"
		},
		"target-without-sha256": func(manifest *ReleaseManifest) {
			manifest.Provenance.SHA256 = "sha256:deadbeef"
		},
		"duplicate-host": func(manifest *ReleaseManifest) {
			manifest.SupportedHosts = append(manifest.SupportedHosts, manifest.SupportedHosts[0])
		},
		"invalid-runtime": func(manifest *ReleaseManifest) {
			manifest.Runtime.DockerMin = "latest"
		},
		"incompatible-generation": func(manifest *ReleaseManifest) {
			manifest.Generation.Spec.Reads.Toolchain = SchemaRange{Min: 1, Max: 3}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := releaseManifestFixture(t, "1.2.3", now)
			mutate(&manifest)
			if _, err := NewReleaseManifest(manifest); err == nil {
				t.Fatalf("%s manifest was accepted", name)
			}
		})
	}

	manifest := releaseManifestFixture(t, "1.2.3", now)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(",\"unknown\":true}")...)
	if _, err = LoadReleaseManifest(raw); err == nil {
		t.Fatal("release manifest with unknown fields was accepted")
	}
	if _, err = LoadReleaseManifest(append(raw, []byte("{}")...)); err == nil {
		t.Fatal("release manifest with trailing JSON was accepted")
	}
}

func TestReleaseIndexIsCanonicalAndBindsManifestIdentity(t *testing.T) {
	now := time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
	firstManifest := releaseManifestFixture(t, "1.2.3", now)
	secondManifest := releaseManifestFixture(t, "1.3.0", now.Add(time.Hour))

	firstDescriptor := releaseManifestDescriptor(t, firstManifest)
	secondDescriptor := releaseManifestDescriptor(t, secondManifest)
	first, err := IndexEntryForManifest(firstManifest, firstDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := IndexEntryForManifest(secondManifest, secondDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, err := EncodeReleaseManifest(firstManifest)
	if err != nil {
		t.Fatal(err)
	}
	if verified, verifyErr := first.VerifyManifest(firstRaw); verifyErr != nil || verified.Generation.ID != firstManifest.Generation.ID {
		t.Fatalf("verified manifest = %#v, %v", verified, verifyErr)
	}
	badDescriptor := firstDescriptor
	badDescriptor.SHA256 = strings.Repeat("9", 64)
	if _, err = IndexEntryForManifest(firstManifest, badDescriptor); err == nil {
		t.Fatal("release index entry accepted a descriptor for different manifest bytes")
	}

	index, err := NewReleaseIndex([]ReleaseIndexEntry{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if index.Entries[0].Release != "1.2.3" || index.Entries[1].Release != "1.3.0" {
		t.Fatalf("release index is not canonical: %#v", index.Entries)
	}
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReleaseIndex(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 2 || loaded.Entries[1].GenerationID != secondManifest.Generation.ID {
		t.Fatalf("loaded release index = %#v", loaded)
	}

	if _, err = NewReleaseIndex([]ReleaseIndexEntry{first, first}); err == nil {
		t.Fatal("duplicate release index entry was accepted")
	}
	badPath := first
	badPath.Manifest.Path = "releases/manifests/other.json"
	if _, err = NewReleaseIndex([]ReleaseIndexEntry{badPath}); err == nil {
		t.Fatal("manifest path that does not match release identity was accepted")
	}
	badGeneration := first
	badGeneration.GenerationID = "sha256:" + strings.Repeat("0", 64)
	if _, err = badGeneration.VerifyManifest(firstRaw); err == nil {
		t.Fatal("release index generation identity drift was accepted")
	}
	badRelease := first
	badRelease.Release = "9.9.9"
	if _, err = badRelease.VerifyManifest(firstRaw); err == nil {
		t.Fatal("release index version drift was accepted")
	}
}
