package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"loki/internal/host/releases"
)

type fakeReleaseSource struct {
	targets map[string]struct {
		descriptor releases.TargetDescriptor
		raw        []byte
	}
	err error
}

func (s *fakeReleaseSource) FetchRelease(_ context.Context, relative string) (releases.TargetDescriptor, []byte, error) {
	if s.err != nil {
		return releases.TargetDescriptor{}, nil, s.err
	}
	target, ok := s.targets[relative]
	if !ok {
		return releases.TargetDescriptor{}, nil, errors.New("fixture target not found")
	}
	return target.descriptor, append([]byte(nil), target.raw...), nil
}

func bootstrapDescriptor(path string, raw []byte) releases.TargetDescriptor {
	sum := sha256.Sum256(raw)
	return releases.TargetDescriptor{
		Path:   path,
		Length: int64(len(raw)),
		SHA256: hex.EncodeToString(sum[:]),
	}
}

func bootstrapGeneration(t *testing.T, version string, releasedAt time.Time, binary []byte) releases.Generation {
	t.Helper()
	sum := sha256.Sum256(binary)
	generation, err := releases.NewGeneration(releases.GenerationSpec{
		Version:          version,
		ReleasedAt:       releasedAt,
		HostBinaryDigest: "sha256:" + hex.EncodeToString(sum[:]),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		ConfigSchema:     1,
		PolicySchema:     1,
		ToolchainSchema:  1,
		StateSchema:      1,
		Reads: releases.Compatibility{
			Config:    releases.SchemaRange{Min: 1, Max: 1},
			Policy:    releases.SchemaRange{Min: 1, Max: 1},
			Toolchain: releases.SchemaRange{Min: 1, Max: 1},
			State:     releases.SchemaRange{Min: 1, Max: 1},
		},
		Rollback: releases.RollbackCoverage{
			StateSnapshot:  true,
			ConfigSnapshot: true,
		},
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
		return releases.TargetDescriptor{
			Path:   targetPath,
			Length: 1,
			SHA256: strings.Repeat(fill, 64),
		}
	}
	manifest := releases.ReleaseManifest{
		Version:          releases.ReleaseManifestVersion,
		Generation:       generation,
		HostBinary:       bootstrapDescriptor("releases/bin/loki-"+version, binary),
		Bootstrap:        descriptor("releases/bootstrap/loki-bootstrap-"+version, "c"),
		HostAssets:       descriptor("releases/assets/loki-host-"+version+".tar.gz", "d"),
		ToolchainCatalog: descriptor("toolchains/catalogs/"+version+".json", "e"),
		Provenance:       descriptor("releases/provenance/"+version+".bundle.json", "f"),
		Notices:          descriptor("releases/notices/"+version+".tar.gz", "1"),
		ReleaseNotes:     descriptor("releases/notes/"+version+".md", "2"),
		SupportedHosts: []releases.SupportedHost{
			{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
			{Environment: "wsl", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
		},
		Runtime: releases.RuntimeRequirements{DockerMin: "28.0.0", ComposeMin: "2.39.0"},
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

func bootstrapSourceFixture(t *testing.T) (*fakeReleaseSource, releases.SupportedHost, []byte, []byte) {
	t.Helper()
	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	firstBinary := []byte("loki-host-v1")
	secondBinary := []byte("loki-host-v2")
	firstManifest, firstRaw := bootstrapManifest(t, "1.0.0", now, firstBinary)
	secondManifest, secondRaw := bootstrapManifest(t, "2.0.0", now.Add(time.Hour), secondBinary)

	firstDescriptor := bootstrapDescriptor("releases/manifests/1.0.0.json", firstRaw)
	secondDescriptor := bootstrapDescriptor("releases/manifests/2.0.0.json", secondRaw)
	firstEntry, err := releases.IndexEntryForManifest(firstManifest, firstDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	secondEntry, err := releases.IndexEntryForManifest(secondManifest, secondDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	index, err := releases.NewReleaseIndex([]releases.ReleaseIndexEntry{secondEntry, firstEntry})
	if err != nil {
		t.Fatal(err)
	}
	indexRaw, err := jsonMarshal(index)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeReleaseSource{targets: map[string]struct {
		descriptor releases.TargetDescriptor
		raw        []byte
	}{
		"index.json": {
			descriptor: bootstrapDescriptor("releases/index.json", indexRaw),
			raw:        indexRaw,
		},
		"manifests/1.0.0.json": {descriptor: firstDescriptor, raw: firstRaw},
		"manifests/2.0.0.json": {descriptor: secondDescriptor, raw: secondRaw},
		"bin/loki-1.0.0":       {descriptor: firstManifest.HostBinary, raw: firstBinary},
		"bin/loki-2.0.0":       {descriptor: secondManifest.HostBinary, raw: secondBinary},
	}}
	host := releases.SupportedHost{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"}
	return source, host, firstBinary, secondBinary
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

func TestResolveSelectsLatestOrExplicitAuthenticatedRelease(t *testing.T) {
	source, host, firstBinary, secondBinary := bootstrapSourceFixture(t)

	latest, err := Resolve(t.Context(), source, host, "")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Entry.Release != "2.0.0" || string(latest.Binary) != string(secondBinary) || latest.Host != host {
		t.Fatalf("latest candidate = %#v", latest)
	}

	explicit, err := Resolve(t.Context(), source, host, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Entry.Release != "1.0.0" || string(explicit.Binary) != string(firstBinary) {
		t.Fatalf("explicit candidate = %#v", explicit)
	}

	if _, err = Resolve(t.Context(), source, host, "9.9.9"); err == nil {
		t.Fatal("release missing from authenticated index was accepted")
	}
}

func TestResolveRejectsDescriptorDriftAndUnsupportedHost(t *testing.T) {
	source, host, _, _ := bootstrapSourceFixture(t)

	t.Run("index-content", func(t *testing.T) {
		changed, _, _, _ := bootstrapSourceFixture(t)
		target := changed.targets["index.json"]
		target.raw = append(target.raw, ' ')
		changed.targets["index.json"] = target
		if _, err := Resolve(t.Context(), changed, host, ""); err == nil {
			t.Fatal("release index bytes that do not match authenticated descriptor were accepted")
		}
	})

	t.Run("manifest-descriptor", func(t *testing.T) {
		changed, _, _, _ := bootstrapSourceFixture(t)
		target := changed.targets["manifests/2.0.0.json"]
		target.descriptor.SHA256 = strings.Repeat("0", 64)
		changed.targets["manifests/2.0.0.json"] = target
		if _, err := Resolve(t.Context(), changed, host, ""); err == nil {
			t.Fatal("release manifest descriptor drift was accepted")
		}
	})

	t.Run("binary-descriptor", func(t *testing.T) {
		changed, _, _, _ := bootstrapSourceFixture(t)
		target := changed.targets["bin/loki-2.0.0"]
		target.descriptor.SHA256 = strings.Repeat("0", 64)
		changed.targets["bin/loki-2.0.0"] = target
		if _, err := Resolve(t.Context(), changed, host, ""); err == nil {
			t.Fatal("host binary descriptor drift was accepted")
		}
	})

	t.Run("unsupported-host", func(t *testing.T) {
		unsupported := host
		unsupported.Arch = "arm64"
		if _, err := Resolve(t.Context(), source, unsupported, ""); err == nil {
			t.Fatal("unsupported host was accepted")
		}
	})
}

func TestDetectHostRecognizesSupportedUbuntuAndWSL(t *testing.T) {
	osRelease := []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n")
	native, err := detectHost("linux", "amd64", osRelease, []byte("6.8.0-generic"))
	if err != nil {
		t.Fatal(err)
	}
	if native != (releases.SupportedHost{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"}) {
		t.Fatalf("native host = %#v", native)
	}
	wsl, err := detectHost("linux", "amd64", osRelease, []byte("6.6.87.2-microsoft-standard-WSL2"))
	if err != nil {
		t.Fatal(err)
	}
	if wsl.Environment != "wsl" {
		t.Fatalf("WSL host = %#v", wsl)
	}

	for name, tc := range map[string]struct {
		goos      string
		goarch    string
		osRelease []byte
	}{
		"wrong-os":     {goos: "darwin", goarch: "amd64", osRelease: osRelease},
		"wrong-arch":   {goos: "linux", goarch: "arm64", osRelease: osRelease},
		"wrong-distro": {goos: "linux", goarch: "amd64", osRelease: []byte("ID=debian\nVERSION_ID=12\n")},
		"wrong-version": {goos: "linux", goarch: "amd64",
			osRelease: []byte("ID=ubuntu\nVERSION_ID=22.04\n")},
		"missing-id": {goos: "linux", goarch: "amd64", osRelease: []byte("VERSION_ID=24.04\n")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := detectHost(tc.goos, tc.goarch, tc.osRelease, nil); err == nil {
				t.Fatalf("%s host was accepted", name)
			}
		})
	}
}
