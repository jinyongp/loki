package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"loki/internal/host/releases"
	"loki/tools/release/internal/bootstrapinfo"
)

func publicationFile(path string, raw []byte) releases.FileEvidence {
	sum := sha256.Sum256(raw)
	return releases.FileEvidence{Path: path, Length: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
}

func publicationTarget(path string, raw []byte) releases.TargetDescriptor {
	sum := sha256.Sum256(raw)
	return releases.TargetDescriptor{Path: path, Length: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
}

func writePublicationCandidate(t *testing.T, commit string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "candidate")
	inputs := filepath.Join(root, "inputs")
	if err := os.MkdirAll(inputs, 0755); err != nil {
		t.Fatal(err)
	}

	hostRaw := []byte("#!/bin/sh\nexit 0\n")
	bootstrapRaw := []byte("#!/bin/sh\nexit 0\n")
	tufRepositoryRaw := []byte("signed-tuf-repository")
	hostAssetsRaw := []byte("host-assets")
	toolchainRaw := []byte("{\"version\":1}\n")
	provenanceRaw := []byte("{\"mediaType\":\"application/vnd.dev.sigstore.bundle.v0.3+json\"}\n")
	noticesRaw := []byte("notices")
	notesRaw := []byte("# Release 1.2.3\n")
	policyRaw := []byte("{\"version\":1}\n")
	configRaw := []byte("version = 1\n")

	previousInspect := inspectPublicationBootstrap
	previousExtract := extractPublicationTUFArchive
	previousTrusted := trustedPublicationTUFRoot
	previousVerify := verifyPublicationTUFDirectory
	inspectPublicationBootstrap = func(context.Context, string) (bootstrapinfo.Info, error) {
		return bootstrapinfo.Info{
			MetadataURL: publicTUFMetadataURL, TrustedRootSHA256: strings.Repeat("a", 64),
		}, nil
	}
	extractPublicationTUFArchive = func(archive, destination string) error {
		raw, err := os.ReadFile(archive)
		if err != nil {
			return err
		}
		if string(raw) != string(tufRepositoryRaw) {
			return fmt.Errorf("unexpected TUF archive bytes")
		}
		for name, body := range map[string]string{
			"1.root.json":    "trusted-root",
			"timestamp.json": "timestamp",
		} {
			if err = os.WriteFile(filepath.Join(destination, name), []byte(body), 0644); err != nil {
				return err
			}
		}
		return nil
	}
	trustedPublicationTUFRoot = func(root, digest string) ([]byte, error) {
		if digest != strings.Repeat("a", 64) {
			return nil, fmt.Errorf("unexpected root digest")
		}
		raw, err := os.ReadFile(filepath.Join(root, "1.root.json"))
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
	verifyPublicationTUFDirectory = func(
		_ context.Context, root string, trusted []byte, requirements []releases.RepositoryRequirement,
	) error {
		if string(trusted) != "trusted-root" || len(requirements) != 9 {
			return fmt.Errorf("unexpected TUF verification inputs")
		}
		if _, err := os.Stat(filepath.Join(root, "timestamp.json")); err != nil {
			return err
		}
		return nil
	}
	t.Cleanup(func() {
		inspectPublicationBootstrap = previousInspect
		extractPublicationTUFArchive = previousExtract
		trustedPublicationTUFRoot = previousTrusted
		verifyPublicationTUFDirectory = previousVerify
	})

	hostSum := sha256.Sum256(hostRaw)
	generation, err := releases.NewGeneration(releases.GenerationSpec{
		Version:          "1.2.3",
		ReleasedAt:       time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
		HostBinaryDigest: "sha256:" + hex.EncodeToString(hostSum[:]),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		Components: []releases.Component{
			{Name: "browser", Digest: "sha256:" + strings.Repeat("c", 64), Optional: true},
		},
		ConfigSchema: 1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: releases.Compatibility{
			Config:    releases.SchemaRange{Min: 1, Max: 1},
			Policy:    releases.SchemaRange{Min: 1, Max: 1},
			Toolchain: releases.SchemaRange{Min: 1, Max: 1},
			State:     releases.SchemaRange{Min: 1, Max: 1},
		},
		Rollback: releases.RollbackCoverage{
			StateSnapshot: true, ConfigSnapshot: true,
			OptionalComponentState: []string{"browser"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := releases.NewReleaseManifest(releases.ReleaseManifest{
		Version: releases.ReleaseManifestVersion, Generation: generation,
		HostBinary:       publicationTarget("releases/bin/loki-1.2.3", hostRaw),
		Bootstrap:        publicationTarget("releases/bootstrap/loki-bootstrap-1.2.3-linux-amd64", bootstrapRaw),
		HostAssets:       publicationTarget("releases/assets/host-1.2.3.tar.gz", hostAssetsRaw),
		ToolchainCatalog: publicationTarget("toolchains/catalogs/1.2.3.json", toolchainRaw),
		Provenance:       publicationTarget("releases/provenance/1.2.3.bundle.json", provenanceRaw),
		Notices:          publicationTarget("releases/notices/1.2.3.tar.gz", noticesRaw),
		ReleaseNotes:     publicationTarget("releases/notes/1.2.3.md", notesRaw),
		SupportedHosts: []releases.SupportedHost{
			{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
			{Environment: "wsl", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
		},
		Runtime: releases.RuntimeRequirements{DockerMin: "28.0.0", ComposeMin: "2.39.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := releases.EncodeReleaseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDescriptor := publicationTarget("releases/manifests/1.2.3.json", manifestRaw)
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

	bodies := map[string]struct {
		raw  []byte
		mode os.FileMode
	}{
		"release-index.json":     {indexRaw, 0644},
		"release-manifest.json":  {manifestRaw, 0644},
		"loki":                   {hostRaw, 0755},
		"loki-bootstrap":         {bootstrapRaw, 0755},
		"tuf-repository.tar.gz":  {tufRepositoryRaw, 0644},
		"host-assets.tar.gz":     {hostAssetsRaw, 0644},
		"toolchain-catalog.json": {toolchainRaw, 0644},
		"provenance.bundle.json": {provenanceRaw, 0644},
		"notices.tar.gz":         {noticesRaw, 0644},
		"release-notes.md":       {notesRaw, 0644},
		"effective-policy.json":  {policyRaw, 0644},
		"effective-config.toml":  {configRaw, 0644},
	}
	for name, body := range bodies {
		if err = os.WriteFile(filepath.Join(inputs, name), body.raw, body.mode); err != nil {
			t.Fatal(err)
		}
	}

	evidence, err := releases.NewCandidateEvidence(releases.CandidateEvidenceInput{
		SourceRevision:   commit,
		Manifest:         manifest,
		IndexEntry:       entry,
		CoreImage:        "ghcr.io/jinyongp/loki@" + generation.Spec.CoreImageDigest,
		BrowserImage:     "ghcr.io/jinyongp/loki-browser@sha256:" + strings.Repeat("c", 64),
		ReleaseIndex:     publicationFile("inputs/release-index.json", indexRaw),
		ReleaseManifest:  publicationFile("inputs/release-manifest.json", manifestRaw),
		HostBinary:       publicationFile("inputs/loki", hostRaw),
		Bootstrap:        publicationFile("inputs/loki-bootstrap", bootstrapRaw),
		TUFRepository:    publicationFile("inputs/tuf-repository.tar.gz", tufRepositoryRaw),
		HostAssets:       publicationFile("inputs/host-assets.tar.gz", hostAssetsRaw),
		ToolchainCatalog: publicationFile("inputs/toolchain-catalog.json", toolchainRaw),
		Provenance:       publicationFile("inputs/provenance.bundle.json", provenanceRaw),
		Notices:          publicationFile("inputs/notices.tar.gz", noticesRaw),
		ReleaseNotes:     publicationFile("inputs/release-notes.md", notesRaw),
		EffectivePolicy:  publicationFile("inputs/effective-policy.json", policyRaw),
		EffectiveConfig:  publicationFile("inputs/effective-config.toml", configRaw),
	})
	if err != nil {
		t.Fatal(err)
	}
	evidenceRaw, err := releases.EncodeCandidateEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "evidence.json"), evidenceRaw, 0644); err != nil {
		t.Fatal(err)
	}

	lines := make([]string, 0, len(bodies)+1)
	for name, body := range bodies {
		sum := sha256.Sum256(body.raw)
		lines = append(lines, fmt.Sprintf("%s  inputs/%s", hex.EncodeToString(sum[:]), name))
	}
	evidenceSum := sha256.Sum256(evidenceRaw)
	lines = append(lines, fmt.Sprintf("%s  evidence.json", hex.EncodeToString(evidenceSum[:])))
	sort.Strings(lines)
	if err = os.WriteFile(filepath.Join(root, "SHA256SUMS"), []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = releases.VerifyCandidateBundle(root); err != nil {
		t.Fatalf("fixture candidate verification: %v", err)
	}
	return root
}

func installerTemplatePath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "tools", "release", "install.sh.tmpl")
}

func TestPreparePublicationProducesReleaseAndPagesInputs(t *testing.T) {
	commit := strings.Repeat("a", 40)
	candidate := writePublicationCandidate(t, commit)
	output := filepath.Join(t.TempDir(), "publication")
	if err := preparePublication(options{
		Candidate:         candidate,
		Output:            output,
		Tag:               "v1.2.3",
		Commit:            commit,
		InstallerTemplate: installerTemplatePath(t),
	}); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"SHA256SUMS",
		"loki-bootstrap-linux-amd64",
		"loki-candidate-evidence.json",
		"loki-host-assets.tar.gz",
		"loki-install.sh",
		"loki-linux-amd64",
		"loki-notices.tar.gz",
		"loki-provenance.bundle.json",
		"loki-release-index.json",
		"loki-release-manifest.json",
		"loki-release-notes.md",
		"loki-toolchain-catalog.json",
		"loki-tuf-repository.tar.gz",
	}
	entries, err := os.ReadDir(filepath.Join(output, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("publication assets = %#v, want %#v", got, want)
	}

	releaseInstaller, err := os.ReadFile(filepath.Join(output, "assets", "loki-install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	pageInstaller, err := os.ReadFile(filepath.Join(output, "pages", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(releaseInstaller) != string(pageInstaller) {
		t.Fatal("release installer and Pages installer differ")
	}
	for _, relative := range []string{
		"pages/tuf/1.root.json",
		"pages/tuf/timestamp.json",
	} {
		if _, err = os.Stat(filepath.Join(output, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("published TUF file %s: %v", relative, err)
		}
	}
	bootstrapRaw, err := os.ReadFile(filepath.Join(candidate, "inputs", "loki-bootstrap"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bootstrapRaw)
	installer := string(releaseInstaller)
	for _, required := range []string{
		"release_tag='v1.2.3'",
		"bootstrap_sha256='" + hex.EncodeToString(sum[:]) + "'",
		"https://github.com/jinyongp/loki/releases/download/$release_tag/$bootstrap_asset",
		"\"$bootstrap\" \"$@\"",
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("installer lacks %q", required)
		}
	}
	if strings.Contains(installer, "@@LOKI_") {
		t.Fatal("rendered installer contains unresolved placeholders")
	}
	if output, err := exec.Command("sh", "-n", filepath.Join(output, "pages", "install.sh")).CombinedOutput(); err != nil {
		t.Fatalf("installer syntax: %v %s", err, output)
	}

	checksums, err := os.ReadFile(filepath.Join(output, "assets", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(checksums), "  loki-bootstrap-linux-amd64\n") ||
		!strings.Contains(string(checksums), "  loki-tuf-repository.tar.gz\n") ||
		!strings.Contains(string(checksums), "  loki-install.sh\n") {
		t.Fatalf("public checksums = %s", checksums)
	}
}

func TestPreparePublicationRejectsIdentityDrift(t *testing.T) {
	commit := strings.Repeat("a", 40)
	candidate := writePublicationCandidate(t, commit)
	template := installerTemplatePath(t)
	tests := map[string]options{
		"tag": {
			Candidate: candidate, Output: filepath.Join(t.TempDir(), "out"),
			Tag: "v9.9.9", Commit: commit, InstallerTemplate: template,
		},
		"commit": {
			Candidate: candidate, Output: filepath.Join(t.TempDir(), "out"),
			Tag: "v1.2.3", Commit: strings.Repeat("b", 40), InstallerTemplate: template,
		},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if err := preparePublication(cfg); err == nil {
				t.Fatalf("%s drift was accepted", name)
			}
		})
	}
}

func TestRenderInstallerRequiresExactPlaceholders(t *testing.T) {
	digest := strings.Repeat("a", 64)
	rendered, err := renderInstaller(
		[]byte("tag=@@LOKI_RELEASE_TAG@@\nsum=@@LOKI_BOOTSTRAP_SHA256@@\n"),
		"v1.2.3", digest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(rendered) != "tag=v1.2.3\nsum="+digest+"\n" {
		t.Fatalf("rendered installer = %q", rendered)
	}
	if _, err = renderInstaller([]byte("@@LOKI_RELEASE_TAG@@"), "v1.2.3", digest); err == nil {
		t.Fatal("template missing digest placeholder was accepted")
	}
}
