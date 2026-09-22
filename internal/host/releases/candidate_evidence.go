package releases

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

const CandidateEvidenceVersion = 2

var sourceRevisionPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type FileEvidence struct {
	Path   string `json:"path"`
	Length int64  `json:"length"`
	SHA256 string `json:"sha256"`
}

type CandidateEvidence struct {
	Version          int          `json:"version"`
	ID               string       `json:"id"`
	SourceRevision   string       `json:"source_revision"`
	Generation       Generation   `json:"generation"`
	CoreImage        string       `json:"core_image"`
	BrowserImage     string       `json:"browser_image,omitempty"`
	ReleaseIndex     FileEvidence `json:"release_index"`
	ReleaseManifest  FileEvidence `json:"release_manifest"`
	HostBinary       FileEvidence `json:"host_binary"`
	Bootstrap        FileEvidence `json:"bootstrap"`
	TUFRepository    FileEvidence `json:"tuf_repository"`
	HostAssets       FileEvidence `json:"host_assets"`
	ToolchainCatalog FileEvidence `json:"toolchain_catalog"`
	Provenance       FileEvidence `json:"provenance"`
	Notices          FileEvidence `json:"notices"`
	ReleaseNotes     FileEvidence `json:"release_notes"`
	EffectivePolicy  FileEvidence `json:"effective_policy"`
	EffectiveConfig  FileEvidence `json:"effective_config"`
}

type CandidateEvidenceInput struct {
	SourceRevision   string
	Manifest         ReleaseManifest
	IndexEntry       ReleaseIndexEntry
	CoreImage        string
	BrowserImage     string
	ReleaseIndex     FileEvidence
	ReleaseManifest  FileEvidence
	HostBinary       FileEvidence
	Bootstrap        FileEvidence
	TUFRepository    FileEvidence
	HostAssets       FileEvidence
	ToolchainCatalog FileEvidence
	Provenance       FileEvidence
	Notices          FileEvidence
	ReleaseNotes     FileEvidence
	EffectivePolicy  FileEvidence
	EffectiveConfig  FileEvidence
}

type candidateEvidencePayload struct {
	Version          int          `json:"version"`
	SourceRevision   string       `json:"source_revision"`
	Generation       Generation   `json:"generation"`
	CoreImage        string       `json:"core_image"`
	BrowserImage     string       `json:"browser_image,omitempty"`
	ReleaseIndex     FileEvidence `json:"release_index"`
	ReleaseManifest  FileEvidence `json:"release_manifest"`
	HostBinary       FileEvidence `json:"host_binary"`
	Bootstrap        FileEvidence `json:"bootstrap"`
	TUFRepository    FileEvidence `json:"tuf_repository"`
	HostAssets       FileEvidence `json:"host_assets"`
	ToolchainCatalog FileEvidence `json:"toolchain_catalog"`
	Provenance       FileEvidence `json:"provenance"`
	Notices          FileEvidence `json:"notices"`
	ReleaseNotes     FileEvidence `json:"release_notes"`
	EffectivePolicy  FileEvidence `json:"effective_policy"`
	EffectiveConfig  FileEvidence `json:"effective_config"`
}

func NewCandidateEvidence(input CandidateEvidenceInput) (CandidateEvidence, error) {
	manifest, err := NewReleaseManifest(input.Manifest)
	if err != nil {
		return CandidateEvidence{}, err
	}
	index, err := NewReleaseIndex([]ReleaseIndexEntry{input.IndexEntry})
	if err != nil {
		return CandidateEvidence{}, err
	}
	entry := index.Entries[0]
	if entry.Release != manifest.Generation.Spec.Version ||
		entry.GenerationID != manifest.Generation.ID ||
		!entry.ReleasedAt.Equal(manifest.Generation.Spec.ReleasedAt) {
		return CandidateEvidence{}, errors.New("release index entry does not match candidate manifest identity")
	}
	sourceRevision := strings.TrimSpace(input.SourceRevision)
	if !sourceRevisionPattern.MatchString(sourceRevision) {
		return CandidateEvidence{}, errors.New("candidate source revision is invalid")
	}
	if err = validateEvidenceImages(manifest.Generation, input.CoreImage, input.BrowserImage); err != nil {
		return CandidateEvidence{}, err
	}

	if err = validateCanonicalEvidencePaths(input.ReleaseIndex, input.ReleaseManifest, input.HostBinary, input.Bootstrap, input.TUFRepository,
		input.HostAssets, input.ToolchainCatalog, input.Provenance, input.Notices, input.ReleaseNotes,
		input.EffectivePolicy, input.EffectiveConfig); err != nil {
		return CandidateEvidence{}, err
	}

	files := map[string]struct {
		evidence FileEvidence
		target   *TargetDescriptor
	}{
		"release index":     {input.ReleaseIndex, nil},
		"release manifest":  {input.ReleaseManifest, &entry.Manifest},
		"host binary":       {input.HostBinary, &manifest.HostBinary},
		"bootstrap":         {input.Bootstrap, &manifest.Bootstrap},
		"TUF repository":    {input.TUFRepository, nil},
		"host assets":       {input.HostAssets, &manifest.HostAssets},
		"toolchain catalog": {input.ToolchainCatalog, &manifest.ToolchainCatalog},
		"provenance":        {input.Provenance, &manifest.Provenance},
		"notices":           {input.Notices, &manifest.Notices},
		"release notes":     {input.ReleaseNotes, &manifest.ReleaseNotes},
		"effective policy":  {input.EffectivePolicy, nil},
		"effective config":  {input.EffectiveConfig, nil},
	}
	seenPaths := map[string]bool{}
	for name, item := range files {
		if err = validateFileEvidence(item.evidence); err != nil {
			return CandidateEvidence{}, errors.New(name + " evidence is invalid: " + err.Error())
		}
		if seenPaths[item.evidence.Path] {
			return CandidateEvidence{}, errors.New("candidate evidence paths must be unique")
		}
		seenPaths[item.evidence.Path] = true
		if item.target != nil &&
			(item.evidence.Length != item.target.Length || item.evidence.SHA256 != item.target.SHA256) {
			return CandidateEvidence{}, errors.New(name + " evidence does not match release target identity")
		}
	}

	payload := candidateEvidencePayload{
		Version: CandidateEvidenceVersion, SourceRevision: sourceRevision, Generation: manifest.Generation,
		CoreImage: input.CoreImage, BrowserImage: input.BrowserImage,
		ReleaseIndex: input.ReleaseIndex, ReleaseManifest: input.ReleaseManifest,
		HostBinary: input.HostBinary, Bootstrap: input.Bootstrap, TUFRepository: input.TUFRepository, HostAssets: input.HostAssets,
		ToolchainCatalog: input.ToolchainCatalog, Provenance: input.Provenance, Notices: input.Notices,
		ReleaseNotes: input.ReleaseNotes, EffectivePolicy: input.EffectivePolicy, EffectiveConfig: input.EffectiveConfig,
	}
	id, err := candidateEvidenceID(payload)
	if err != nil {
		return CandidateEvidence{}, err
	}
	return CandidateEvidence{
		Version: payload.Version, ID: id, SourceRevision: payload.SourceRevision, Generation: payload.Generation,
		CoreImage: payload.CoreImage, BrowserImage: payload.BrowserImage,
		ReleaseIndex: payload.ReleaseIndex, ReleaseManifest: payload.ReleaseManifest,
		HostBinary: payload.HostBinary, Bootstrap: payload.Bootstrap, TUFRepository: payload.TUFRepository, HostAssets: payload.HostAssets,
		ToolchainCatalog: payload.ToolchainCatalog, Provenance: payload.Provenance, Notices: payload.Notices,
		ReleaseNotes: payload.ReleaseNotes, EffectivePolicy: payload.EffectivePolicy, EffectiveConfig: payload.EffectiveConfig,
	}, nil
}

func LoadCandidateEvidence(raw []byte) (CandidateEvidence, error) {
	var evidence CandidateEvidence
	if err := decodeStrictJSON(raw, &evidence, "release candidate evidence"); err != nil {
		return CandidateEvidence{}, err
	}
	if evidence.Version != CandidateEvidenceVersion || !digestPattern.MatchString(evidence.ID) ||
		!sourceRevisionPattern.MatchString(evidence.SourceRevision) || !evidence.Generation.Valid() {
		return CandidateEvidence{}, errors.New("release candidate evidence identity is invalid")
	}
	if err := validateEvidenceImages(evidence.Generation, evidence.CoreImage, evidence.BrowserImage); err != nil {
		return CandidateEvidence{}, err
	}
	if err := validateCanonicalEvidencePaths(evidence.ReleaseIndex, evidence.ReleaseManifest, evidence.HostBinary, evidence.Bootstrap, evidence.TUFRepository,
		evidence.HostAssets, evidence.ToolchainCatalog, evidence.Provenance, evidence.Notices, evidence.ReleaseNotes,
		evidence.EffectivePolicy, evidence.EffectiveConfig); err != nil {
		return CandidateEvidence{}, err
	}
	seenPaths := map[string]bool{}
	for _, item := range []FileEvidence{
		evidence.ReleaseIndex, evidence.ReleaseManifest, evidence.HostBinary, evidence.Bootstrap, evidence.TUFRepository,
		evidence.HostAssets, evidence.ToolchainCatalog, evidence.Provenance, evidence.Notices,
		evidence.ReleaseNotes, evidence.EffectivePolicy, evidence.EffectiveConfig,
	} {
		if err := validateFileEvidence(item); err != nil {
			return CandidateEvidence{}, err
		}
		if seenPaths[item.Path] {
			return CandidateEvidence{}, errors.New("release candidate evidence paths must be unique")
		}
		seenPaths[item.Path] = true
	}
	payload := candidateEvidencePayload{
		Version: evidence.Version, SourceRevision: evidence.SourceRevision, Generation: evidence.Generation,
		CoreImage: evidence.CoreImage, BrowserImage: evidence.BrowserImage,
		ReleaseIndex: evidence.ReleaseIndex, ReleaseManifest: evidence.ReleaseManifest,
		HostBinary: evidence.HostBinary, Bootstrap: evidence.Bootstrap, TUFRepository: evidence.TUFRepository, HostAssets: evidence.HostAssets,
		ToolchainCatalog: evidence.ToolchainCatalog, Provenance: evidence.Provenance, Notices: evidence.Notices,
		ReleaseNotes: evidence.ReleaseNotes, EffectivePolicy: evidence.EffectivePolicy, EffectiveConfig: evidence.EffectiveConfig,
	}
	id, err := candidateEvidenceID(payload)
	if err != nil {
		return CandidateEvidence{}, err
	}
	if evidence.ID != id {
		return CandidateEvidence{}, errors.New("release candidate evidence content identity does not match")
	}
	return evidence, nil
}

func EncodeCandidateEvidence(evidence CandidateEvidence) ([]byte, error) {
	raw, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	if _, err = LoadCandidateEvidence(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func candidateEvidenceID(payload candidateEvidencePayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validateCanonicalEvidencePaths(
	releaseIndex, releaseManifest, hostBinary, bootstrap, tufRepository, hostAssets, toolchainCatalog,
	provenance, notices, releaseNotes, effectivePolicy, effectiveConfig FileEvidence,
) error {
	expected := map[string]string{
		"release index":     releaseIndex.Path,
		"release manifest":  releaseManifest.Path,
		"host binary":       hostBinary.Path,
		"bootstrap":         bootstrap.Path,
		"TUF repository":    tufRepository.Path,
		"host assets":       hostAssets.Path,
		"toolchain catalog": toolchainCatalog.Path,
		"provenance":        provenance.Path,
		"notices":           notices.Path,
		"release notes":     releaseNotes.Path,
		"effective policy":  effectivePolicy.Path,
		"effective config":  effectiveConfig.Path,
	}
	canonical := map[string]string{
		"release index":     "inputs/release-index.json",
		"release manifest":  "inputs/release-manifest.json",
		"host binary":       "inputs/loki",
		"bootstrap":         "inputs/loki-bootstrap",
		"TUF repository":    "inputs/tuf-repository.tar.gz",
		"host assets":       "inputs/host-assets.tar.gz",
		"toolchain catalog": "inputs/toolchain-catalog.json",
		"provenance":        "inputs/provenance.bundle.json",
		"notices":           "inputs/notices.tar.gz",
		"release notes":     "inputs/release-notes.md",
		"effective policy":  "inputs/effective-policy.json",
		"effective config":  "inputs/effective-config.toml",
	}
	for name, path := range expected {
		if path != canonical[name] {
			return errors.New("candidate " + name + " evidence path is not canonical")
		}
	}
	return nil
}

func validateFileEvidence(evidence FileEvidence) error {
	clean := path.Clean(strings.TrimSpace(evidence.Path))
	if clean == "." || clean != evidence.Path || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") ||
		evidence.Length <= 0 || evidence.Length > maxTargetBytes || !sha256Pattern.MatchString(evidence.SHA256) {
		return errors.New("file evidence is invalid")
	}
	return nil
}

func validateEvidenceImages(generation Generation, coreImage, browserImage string) error {
	if !generation.Valid() {
		return errors.New("candidate release generation is invalid")
	}
	if err := validateImageReference(coreImage, generation.Spec.CoreImageDigest); err != nil {
		return err
	}
	browserDigest := ""
	for _, component := range generation.Spec.Components {
		if component.Name == "browser" {
			browserDigest = component.Digest
			break
		}
	}
	switch {
	case browserDigest != "" && browserImage == "":
		return errors.New("candidate browser image reference is required")
	case browserDigest == "" && browserImage != "":
		return errors.New("candidate browser image is not declared by the release generation")
	case browserDigest != "":
		return validateImageReference(browserImage, browserDigest)
	default:
		return nil
	}
}

func validateImageReference(reference, expectedDigest string) error {
	trimmed := strings.TrimSpace(reference)
	if reference != trimmed || strings.Contains(trimmed, "://") {
		return errors.New("candidate OCI image reference does not match release generation digest")
	}
	parsed, err := name.NewDigest(trimmed, name.StrictValidation)
	if err != nil || parsed.DigestStr() != expectedDigest {
		return errors.New("candidate OCI image reference does not match release generation digest")
	}
	return nil
}
