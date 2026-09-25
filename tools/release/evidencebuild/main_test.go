package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/host/releases"
	"loki/tools/release/internal/bootstrapinfo"
)

func writeFixtureFile(t *testing.T, root, name, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func descriptorFor(body, target string) releases.TargetDescriptor {
	sum := sha256.Sum256([]byte(body))
	return releases.TargetDescriptor{Path: target, Length: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}

func evidenceAssemblerFixture(t *testing.T) options {
	t.Helper()
	root := t.TempDir()
	hostBody := "#!/bin/sh\nexit 0\n"
	bootstrapBody := "#!/bin/sh\nexit 0\n"
	assetsBody := "host-assets"
	wslBody := "synthetic-wsl-appliance"
	toolchainBody := "{\"version\":1}\n"
	provenanceBody := "{\"_type\":\"https://in-toto.io/Statement/v1\"}\n"
	noticesBody := "notices"
	notesBody := "# Release 1.2.3\n"
	policyBody := "{\"version\":1,\"allowed_hosts\":[\"registry.npmjs.org\"]}\n"
	configBody := "version = 1\n"

	host := writeFixtureFile(t, root, "host/loki", hostBody, 0755)
	bootstrap := writeFixtureFile(t, root, "host/loki-bootstrap", bootstrapBody, 0755)
	assets := writeFixtureFile(t, root, "host/assets.tar.gz", assetsBody, 0644)
	wsl := writeFixtureFile(t, root, "host/loki-wsl-amd64.wsl", wslBody, 0644)
	toolchain := writeFixtureFile(t, root, "host/toolchain.json", toolchainBody, 0644)
	provenance := writeFixtureFile(t, root, "host/provenance.json", provenanceBody, 0644)
	notices := writeFixtureFile(t, root, "host/notices.tar.gz", noticesBody, 0644)
	notes := writeFixtureFile(t, root, "host/notes.md", notesBody, 0644)
	policy := writeFixtureFile(t, root, "host/policy.json", policyBody, 0644)
	config := writeFixtureFile(t, root, "host/config.toml", configBody, 0644)

	hostSum := sha256.Sum256([]byte(hostBody))
	generation, err := releases.NewGeneration(releases.GenerationSpec{
		Version:          "1.2.3",
		ReleasedAt:       time.Date(2026, 9, 22, 7, 30, 0, 0, time.UTC),
		HostBinaryDigest: "sha256:" + hex.EncodeToString(hostSum[:]),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		Components: []releases.Component{
			{Name: "browser", Digest: "sha256:" + strings.Repeat("c", 64), Optional: true},
			{Name: "signing", Digest: "sha256:" + strings.Repeat("d", 64), Optional: true},
		},
		ConfigSchema: 1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: releases.Compatibility{
			Config: releases.SchemaRange{Min: 1, Max: 1}, Policy: releases.SchemaRange{Min: 1, Max: 1},
			Toolchain: releases.SchemaRange{Min: 1, Max: 1}, State: releases.SchemaRange{Min: 1, Max: 1},
		},
		Rollback: releases.RollbackCoverage{
			StateSnapshot: true, ConfigSnapshot: true,
			OptionalComponentState: []string{"browser", "signing"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := releases.NewReleaseManifest(releases.ReleaseManifest{
		Version: releases.ReleaseManifestVersion, Generation: generation,
		HostBinary:       descriptorFor(hostBody, "releases/bin/loki-1.2.3"),
		HostAssets:       descriptorFor(assetsBody, "releases/assets/host-1.2.3.tar.gz"),
		ToolchainCatalog: descriptorFor(toolchainBody, "toolchains/catalogs/1.2.3.json"),
		Provenance:       descriptorFor(provenanceBody, "releases/provenance/1.2.3.bundle.json"),
		Notices:          descriptorFor(noticesBody, "releases/notices/1.2.3.tar.gz"),
		ReleaseNotes:     descriptorFor(notesBody, "releases/notes/1.2.3.md"),
		SupportedHosts: []releases.SupportedHost{
			{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
			{Environment: "wsl", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
		},
		Runtime: releases.RuntimeRequirements{DockerMin: "29.8.1", ComposeMin: "5.5.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := releases.EncodeReleaseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := writeFixtureFile(t, root, "metadata/manifest.json", string(manifestRaw), 0644)
	manifestDescriptor := descriptorFor(string(manifestRaw), "releases/manifests/1.2.3.json")
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
	indexPath := writeFixtureFile(t, root, "metadata/index.json", string(indexRaw), 0644)

	manifestSum := sha256.Sum256(manifestRaw)
	previousInspect := inspectBootstrapRelease
	inspectBootstrapRelease = func(context.Context, string) (bootstrapinfo.Info, error) {
		return bootstrapinfo.Info{
			ReleaseTag:            "v1.2.3",
			ReleaseManifestSHA256: hex.EncodeToString(manifestSum[:]),
			HostBinarySHA256:      manifest.HostBinary.SHA256,
		}, nil
	}
	t.Cleanup(func() { inspectBootstrapRelease = previousInspect })

	return options{
		Output:         filepath.Join(root, "candidate-evidence"),
		SourceRevision: strings.Repeat("a", 40),
		ReleaseIndex:   indexPath, ReleaseManifest: manifestPath,
		HostBinary: host, Bootstrap: bootstrap, HostAssets: assets, WSLAppliance: wsl,
		ToolchainCatalog: toolchain, Provenance: provenance, Notices: notices,
		ReleaseNotes: notes, EffectivePolicy: policy, EffectiveConfig: config,
		CoreImage:    "ghcr.io/example/loki@sha256:" + strings.Repeat("b", 64),
		BrowserImage: "ghcr.io/example/loki-browser@sha256:" + strings.Repeat("c", 64),
	}
}

func TestAssembleProducesSelfContainedImmutableEvidenceBundle(t *testing.T) {
	cfg := evidenceAssemblerFixture(t)
	if err := assemble(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.Output, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := releases.LoadCandidateEvidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := releases.VerifyCandidateBundle(cfg.Output)
	if err != nil {
		t.Fatal(err)
	}
	if verified.ID != evidence.ID || evidence.SourceRevision != cfg.SourceRevision || evidence.Generation.Spec.Version != "1.2.3" {
		t.Fatalf("evidence = %#v", evidence)
	}
	for _, relative := range []string{
		"evidence.json", "SHA256SUMS", "inputs/release-index.json", "inputs/release-manifest.json",
		"inputs/loki", "inputs/loki-bootstrap", "inputs/host-assets.tar.gz", "inputs/loki-wsl-amd64.wsl", "inputs/toolchain-catalog.json",
		"inputs/provenance.bundle.json", "inputs/notices.tar.gz", "inputs/release-notes.md",
		"inputs/effective-policy.json", "inputs/effective-config.toml",
	} {
		if _, err = os.Stat(filepath.Join(cfg.Output, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("bundle file %s: %v", relative, err)
		}
	}
	checksums, err := os.ReadFile(filepath.Join(cfg.Output, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"evidence.json", "inputs/loki", "inputs/loki-bootstrap", "inputs/loki-wsl-amd64.wsl", "inputs/release-manifest.json"} {
		if !strings.Contains(string(checksums), "  "+required+"\n") {
			t.Fatalf("SHA256SUMS lacks %s", required)
		}
	}
	if err = assemble(cfg); err == nil {
		t.Fatal("assembler overwrote an existing evidence bundle")
	}
}

func TestAssembleNormalizesBundleModesDespiteUmask(t *testing.T) {
	cfg := evidenceAssemblerFixture(t)
	oldUmask := unix.Umask(0077)
	err := assemble(cfg)
	unix.Umask(oldUmask)
	if err != nil {
		t.Fatal(err)
	}
	for relative, want := range map[string]os.FileMode{
		".": 0755, "inputs": 0755, "inputs/loki": 0755, "inputs/loki-bootstrap": 0755,
		"inputs/release-index.json": 0644, "inputs/loki-wsl-amd64.wsl": 0644, "inputs/effective-policy.json": 0644,
		"evidence.json": 0644, "SHA256SUMS": 0644,
	} {
		info, statErr := os.Stat(filepath.Join(cfg.Output, filepath.FromSlash(relative)))
		if statErr != nil {
			t.Fatalf("stat %s: %v", relative, statErr)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", relative, got, want)
		}
	}
}

func TestAssembleRejectsReleaseBindingDrift(t *testing.T) {
	cfg := evidenceAssemblerFixture(t)
	previous := inspectBootstrapRelease
	inspectBootstrapRelease = func(context.Context, string) (bootstrapinfo.Info, error) {
		return bootstrapinfo.Info{
			ReleaseTag:            "v9.9.9",
			ReleaseManifestSHA256: strings.Repeat("a", 64),
			HostBinarySHA256:      strings.Repeat("b", 64),
		}, nil
	}
	defer func() { inspectBootstrapRelease = previous }()
	if err := assemble(cfg); err == nil {
		t.Fatal("assembler accepted bootstrap release binding drift")
	}
}

func TestVerifyCandidateBundleRejectsCopiedInputTampering(t *testing.T) {
	cfg := evidenceAssemblerFixture(t)
	if err := assemble(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.Output, "inputs", "effective-policy.json")
	if err := os.WriteFile(path, []byte("{\"version\":9}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := releases.VerifyCandidateBundle(cfg.Output); err == nil {
		t.Fatal("bundle verifier accepted a tampered copied input")
	}
}

func TestAssembleRejectsMutableOrTamperedCandidateInputs(t *testing.T) {
	t.Run("mutable-image", func(t *testing.T) {
		cfg := evidenceAssemblerFixture(t)
		cfg.CoreImage = "ghcr.io/example/loki:latest"
		if err := assemble(cfg); err == nil {
			t.Fatal("assembler accepted a mutable core image reference")
		}
	})
	t.Run("tampered-host", func(t *testing.T) {
		cfg := evidenceAssemblerFixture(t)
		if err := os.WriteFile(cfg.HostBinary, []byte("#!/bin/sh\nexit 9\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := assemble(cfg); err == nil {
			t.Fatal("assembler accepted a host binary that does not match the manifest")
		}
	})
}

func TestPublishEvidenceBundleNeverReplacesExistingOutput(t *testing.T) {
	parent := t.TempDir()
	staging := filepath.Join(parent, "staging")
	output := filepath.Join(parent, "candidate")
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(output, "marker")
	if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishEvidenceBundle(staging, output); err == nil {
		t.Fatal("evidence publication replaced an existing output")
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "preserve" {
		t.Fatalf("existing output changed: data=%q err=%v", raw, err)
	}
}
