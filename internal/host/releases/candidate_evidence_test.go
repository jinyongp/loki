package releases

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func candidateFile(path string, length int64, sha string) FileEvidence {
	return FileEvidence{Path: path, Length: length, SHA256: sha}
}

func candidateEvidenceFixture(t *testing.T) CandidateEvidenceInput {
	t.Helper()
	manifest := releaseManifestFixture(t, "1.2.3", time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC))
	manifestDescriptor := releaseManifestDescriptor(t, manifest)
	entry, err := IndexEntryForManifest(manifest, manifestDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	browserDigest := ""
	for _, component := range manifest.Generation.Spec.Components {
		if component.Name == "browser" {
			browserDigest = component.Digest
		}
	}
	return CandidateEvidenceInput{
		SourceRevision:  strings.Repeat("a", 40),
		Manifest:        manifest,
		IndexEntry:      entry,
		CoreImage:       "ghcr.io/example/loki@" + manifest.Generation.Spec.CoreImageDigest,
		BrowserImage:    "ghcr.io/example/loki-browser@" + browserDigest,
		ReleaseIndex:    candidateFile("inputs/release-index.json", 321, strings.Repeat("9", 64)),
		ReleaseManifest: candidateFile("inputs/release-manifest.json", manifestDescriptor.Length, manifestDescriptor.SHA256),
		HostBinary: candidateFile(
			"inputs/loki", manifest.HostBinary.Length, manifest.HostBinary.SHA256,
		),
		Bootstrap: candidateFile(
			"inputs/loki-bootstrap", 456, strings.Repeat("6", 64),
		),
		HostAssets: candidateFile(
			"inputs/host-assets.tar.gz", manifest.HostAssets.Length, manifest.HostAssets.SHA256,
		),
		ToolchainCatalog: candidateFile(
			"inputs/toolchain-catalog.json", manifest.ToolchainCatalog.Length, manifest.ToolchainCatalog.SHA256,
		),
		Provenance: candidateFile(
			"inputs/provenance.bundle.json", manifest.Provenance.Length, manifest.Provenance.SHA256,
		),
		Notices: candidateFile(
			"inputs/notices.tar.gz", manifest.Notices.Length, manifest.Notices.SHA256,
		),
		ReleaseNotes: candidateFile(
			"inputs/release-notes.md", manifest.ReleaseNotes.Length, manifest.ReleaseNotes.SHA256,
		),
		EffectivePolicy: candidateFile(
			"inputs/effective-policy.json", 55, strings.Repeat("7", 64),
		),
		EffectiveConfig: candidateFile(
			"inputs/effective-config.toml", 66, strings.Repeat("8", 64),
		),
	}
}

func TestCandidateEvidenceBindsReleaseFilesImagesAndSchemas(t *testing.T) {
	input := candidateEvidenceFixture(t)
	evidence, err := NewCandidateEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Generation.Spec.Version != "1.2.3" || evidence.Generation.ID != input.Manifest.Generation.ID ||
		evidence.Generation.Spec.ConfigSchema != input.Manifest.Generation.Spec.ConfigSchema ||
		evidence.Generation.Spec.PolicySchema != input.Manifest.Generation.Spec.PolicySchema ||
		!digestPattern.MatchString(evidence.ID) {
		t.Fatalf("candidate evidence = %#v", evidence)
	}
	raw, err := EncodeCandidateEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCandidateEvidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, evidence) {
		t.Fatalf("loaded evidence = %#v, want %#v", loaded, evidence)
	}

	var tampered map[string]any
	if err = json.Unmarshal(raw, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["source_revision"] = strings.Repeat("b", 40)
	raw, err = json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = LoadCandidateEvidence(raw); err == nil {
		t.Fatal("candidate evidence accepted content with stale bundle identity")
	}
}

func TestCandidateEvidenceRejectsReleaseIdentityDrift(t *testing.T) {
	tests := map[string]func(*CandidateEvidenceInput){
		"source-revision": func(input *CandidateEvidenceInput) {
			input.SourceRevision = "main"
		},
		"core-image": func(input *CandidateEvidenceInput) {
			input.CoreImage = "ghcr.io/example/loki@sha256:" + strings.Repeat("f", 64)
		},
		"core-image-scheme": func(input *CandidateEvidenceInput) {
			input.CoreImage = "https://ghcr.io/example/loki@" + input.Manifest.Generation.Spec.CoreImageDigest
		},
		"browser-image": func(input *CandidateEvidenceInput) {
			input.BrowserImage = "ghcr.io/example/loki-browser@sha256:" + strings.Repeat("f", 64)
		},
		"host-binary": func(input *CandidateEvidenceInput) {
			input.HostBinary.SHA256 = strings.Repeat("f", 64)
		},
		"manifest": func(input *CandidateEvidenceInput) {
			input.ReleaseManifest.Length++
		},
		"duplicate-path": func(input *CandidateEvidenceInput) {
			input.EffectiveConfig.Path = input.EffectivePolicy.Path
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := candidateEvidenceFixture(t)
			mutate(&input)
			if _, err := NewCandidateEvidence(input); err == nil {
				t.Fatal("candidate evidence accepted release identity drift")
			}
		})
	}
}
