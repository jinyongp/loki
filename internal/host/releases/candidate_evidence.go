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

const CandidateEvidenceVersion = 5

var sourceRevisionPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type FileEvidence struct {
	Path   string `json:"path"`
	Length int64  `json:"length"`
	SHA256 string `json:"sha256"`
}

type CandidateEvidence struct {
	Version              int          `json:"version"`
	ID                   string       `json:"id"`
	SourceRevision       string       `json:"source_revision"`
	Generation           Generation   `json:"generation"`
	CoreImage            string       `json:"core_image"`
	BrowserImage         string       `json:"browser_image,omitempty"`
	ReleaseIndex         FileEvidence `json:"release_index"`
	ReleaseManifest      FileEvidence `json:"release_manifest"`
	HostBinary           FileEvidence `json:"host_binary"`
	Bootstrap            FileEvidence `json:"bootstrap"`
	HostAssets           FileEvidence `json:"host_assets"`
	WSLAppliance         FileEvidence `json:"wsl_appliance"`
	WindowsFrontend      FileEvidence `json:"windows_frontend"`
	ConnectHelperCatalog FileEvidence `json:"connect_helper_catalog"`
	ConnectHelperArchive FileEvidence `json:"connect_helper_archive"`
	ConnectHelperLicense FileEvidence `json:"connect_helper_license"`
	ConnectHelperNotice  FileEvidence `json:"connect_helper_notice"`
	ConnectHelperSPDX    FileEvidence `json:"connect_helper_spdx"`
	ToolchainCatalog     FileEvidence `json:"toolchain_catalog"`
	Provenance           FileEvidence `json:"provenance"`
	Notices              FileEvidence `json:"notices"`
	ReleaseNotes         FileEvidence `json:"release_notes"`
	EffectivePolicy      FileEvidence `json:"effective_policy"`
	EffectiveConfig      FileEvidence `json:"effective_config"`
}

type CandidateEvidenceInput struct {
	SourceRevision       string
	Manifest             ReleaseManifest
	IndexEntry           ReleaseIndexEntry
	CoreImage            string
	BrowserImage         string
	ReleaseIndex         FileEvidence
	ReleaseManifest      FileEvidence
	HostBinary           FileEvidence
	Bootstrap            FileEvidence
	HostAssets           FileEvidence
	WSLAppliance         FileEvidence
	WindowsFrontend      FileEvidence
	ConnectHelperCatalog FileEvidence
	ConnectHelperArchive FileEvidence
	ConnectHelperLicense FileEvidence
	ConnectHelperNotice  FileEvidence
	ConnectHelperSPDX    FileEvidence
	ToolchainCatalog     FileEvidence
	Provenance           FileEvidence
	Notices              FileEvidence
	ReleaseNotes         FileEvidence
	EffectivePolicy      FileEvidence
	EffectiveConfig      FileEvidence
}

type candidateEvidencePayload struct {
	Version              int          `json:"version"`
	SourceRevision       string       `json:"source_revision"`
	Generation           Generation   `json:"generation"`
	CoreImage            string       `json:"core_image"`
	BrowserImage         string       `json:"browser_image,omitempty"`
	ReleaseIndex         FileEvidence `json:"release_index"`
	ReleaseManifest      FileEvidence `json:"release_manifest"`
	HostBinary           FileEvidence `json:"host_binary"`
	Bootstrap            FileEvidence `json:"bootstrap"`
	HostAssets           FileEvidence `json:"host_assets"`
	WSLAppliance         FileEvidence `json:"wsl_appliance"`
	WindowsFrontend      FileEvidence `json:"windows_frontend"`
	ConnectHelperCatalog FileEvidence `json:"connect_helper_catalog"`
	ConnectHelperArchive FileEvidence `json:"connect_helper_archive"`
	ConnectHelperLicense FileEvidence `json:"connect_helper_license"`
	ConnectHelperNotice  FileEvidence `json:"connect_helper_notice"`
	ConnectHelperSPDX    FileEvidence `json:"connect_helper_spdx"`
	ToolchainCatalog     FileEvidence `json:"toolchain_catalog"`
	Provenance           FileEvidence `json:"provenance"`
	Notices              FileEvidence `json:"notices"`
	ReleaseNotes         FileEvidence `json:"release_notes"`
	EffectivePolicy      FileEvidence `json:"effective_policy"`
	EffectiveConfig      FileEvidence `json:"effective_config"`
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

	if err = validateCanonicalEvidencePaths(candidateFilesFromInput(input)); err != nil {
		return CandidateEvidence{}, err
	}

	files := map[string]struct {
		evidence FileEvidence
		target   *TargetDescriptor
	}{
		"release index":          {input.ReleaseIndex, nil},
		"release manifest":       {input.ReleaseManifest, &entry.Manifest},
		"host binary":            {input.HostBinary, &manifest.HostBinary},
		"bootstrap":              {input.Bootstrap, nil},
		"host assets":            {input.HostAssets, &manifest.HostAssets},
		"WSL appliance":          {input.WSLAppliance, nil},
		"Windows frontend":       {input.WindowsFrontend, nil},
		"connect helper catalog": {input.ConnectHelperCatalog, nil},
		"connect helper archive": {input.ConnectHelperArchive, nil},
		"connect helper license": {input.ConnectHelperLicense, nil},
		"connect helper notice":  {input.ConnectHelperNotice, nil},
		"connect helper SPDX":    {input.ConnectHelperSPDX, nil},
		"toolchain catalog":      {input.ToolchainCatalog, &manifest.ToolchainCatalog},
		"provenance":             {input.Provenance, &manifest.Provenance},
		"notices":                {input.Notices, &manifest.Notices},
		"release notes":          {input.ReleaseNotes, &manifest.ReleaseNotes},
		"effective policy":       {input.EffectivePolicy, nil},
		"effective config":       {input.EffectiveConfig, nil},
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
		HostBinary: input.HostBinary, Bootstrap: input.Bootstrap, HostAssets: input.HostAssets, WSLAppliance: input.WSLAppliance,
		WindowsFrontend: input.WindowsFrontend, ConnectHelperCatalog: input.ConnectHelperCatalog,
		ConnectHelperArchive: input.ConnectHelperArchive, ConnectHelperLicense: input.ConnectHelperLicense,
		ConnectHelperNotice: input.ConnectHelperNotice, ConnectHelperSPDX: input.ConnectHelperSPDX,
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
		HostBinary: payload.HostBinary, Bootstrap: payload.Bootstrap, HostAssets: payload.HostAssets, WSLAppliance: payload.WSLAppliance,
		WindowsFrontend: payload.WindowsFrontend, ConnectHelperCatalog: payload.ConnectHelperCatalog,
		ConnectHelperArchive: payload.ConnectHelperArchive, ConnectHelperLicense: payload.ConnectHelperLicense,
		ConnectHelperNotice: payload.ConnectHelperNotice, ConnectHelperSPDX: payload.ConnectHelperSPDX,
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
	if err := validateCanonicalEvidencePaths(candidateFilesFromEvidence(evidence)); err != nil {
		return CandidateEvidence{}, err
	}
	seenPaths := map[string]bool{}
	for _, item := range []FileEvidence{
		evidence.ReleaseIndex, evidence.ReleaseManifest, evidence.HostBinary, evidence.Bootstrap,
		evidence.HostAssets, evidence.WSLAppliance, evidence.WindowsFrontend, evidence.ConnectHelperCatalog,
		evidence.ConnectHelperArchive, evidence.ConnectHelperLicense, evidence.ConnectHelperNotice, evidence.ConnectHelperSPDX,
		evidence.ToolchainCatalog, evidence.Provenance, evidence.Notices, evidence.ReleaseNotes, evidence.EffectivePolicy, evidence.EffectiveConfig,
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
		HostBinary: evidence.HostBinary, Bootstrap: evidence.Bootstrap, HostAssets: evidence.HostAssets, WSLAppliance: evidence.WSLAppliance,
		WindowsFrontend: evidence.WindowsFrontend, ConnectHelperCatalog: evidence.ConnectHelperCatalog,
		ConnectHelperArchive: evidence.ConnectHelperArchive, ConnectHelperLicense: evidence.ConnectHelperLicense,
		ConnectHelperNotice: evidence.ConnectHelperNotice, ConnectHelperSPDX: evidence.ConnectHelperSPDX,
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

type candidateEvidenceFiles struct {
	ReleaseIndex         FileEvidence
	ReleaseManifest      FileEvidence
	HostBinary           FileEvidence
	Bootstrap            FileEvidence
	HostAssets           FileEvidence
	WSLAppliance         FileEvidence
	WindowsFrontend      FileEvidence
	ConnectHelperCatalog FileEvidence
	ConnectHelperArchive FileEvidence
	ConnectHelperLicense FileEvidence
	ConnectHelperNotice  FileEvidence
	ConnectHelperSPDX    FileEvidence
	ToolchainCatalog     FileEvidence
	Provenance           FileEvidence
	Notices              FileEvidence
	ReleaseNotes         FileEvidence
	EffectivePolicy      FileEvidence
	EffectiveConfig      FileEvidence
}

func candidateFilesFromInput(input CandidateEvidenceInput) candidateEvidenceFiles {
	return candidateEvidenceFiles{
		ReleaseIndex: input.ReleaseIndex, ReleaseManifest: input.ReleaseManifest,
		HostBinary: input.HostBinary, Bootstrap: input.Bootstrap, HostAssets: input.HostAssets,
		WSLAppliance: input.WSLAppliance, WindowsFrontend: input.WindowsFrontend,
		ConnectHelperCatalog: input.ConnectHelperCatalog, ConnectHelperArchive: input.ConnectHelperArchive,
		ConnectHelperLicense: input.ConnectHelperLicense, ConnectHelperNotice: input.ConnectHelperNotice,
		ConnectHelperSPDX: input.ConnectHelperSPDX, ToolchainCatalog: input.ToolchainCatalog,
		Provenance: input.Provenance, Notices: input.Notices, ReleaseNotes: input.ReleaseNotes,
		EffectivePolicy: input.EffectivePolicy, EffectiveConfig: input.EffectiveConfig,
	}
}

func candidateFilesFromEvidence(evidence CandidateEvidence) candidateEvidenceFiles {
	return candidateEvidenceFiles{
		ReleaseIndex: evidence.ReleaseIndex, ReleaseManifest: evidence.ReleaseManifest,
		HostBinary: evidence.HostBinary, Bootstrap: evidence.Bootstrap, HostAssets: evidence.HostAssets,
		WSLAppliance: evidence.WSLAppliance, WindowsFrontend: evidence.WindowsFrontend,
		ConnectHelperCatalog: evidence.ConnectHelperCatalog, ConnectHelperArchive: evidence.ConnectHelperArchive,
		ConnectHelperLicense: evidence.ConnectHelperLicense, ConnectHelperNotice: evidence.ConnectHelperNotice,
		ConnectHelperSPDX: evidence.ConnectHelperSPDX, ToolchainCatalog: evidence.ToolchainCatalog,
		Provenance: evidence.Provenance, Notices: evidence.Notices, ReleaseNotes: evidence.ReleaseNotes,
		EffectivePolicy: evidence.EffectivePolicy, EffectiveConfig: evidence.EffectiveConfig,
	}
}

func validateCanonicalEvidencePaths(files candidateEvidenceFiles) error {
	expected := map[string]string{
		"release index":          files.ReleaseIndex.Path,
		"release manifest":       files.ReleaseManifest.Path,
		"host binary":            files.HostBinary.Path,
		"bootstrap":              files.Bootstrap.Path,
		"host assets":            files.HostAssets.Path,
		"WSL appliance":          files.WSLAppliance.Path,
		"Windows frontend":       files.WindowsFrontend.Path,
		"connect helper catalog": files.ConnectHelperCatalog.Path,
		"connect helper archive": files.ConnectHelperArchive.Path,
		"connect helper license": files.ConnectHelperLicense.Path,
		"connect helper notice":  files.ConnectHelperNotice.Path,
		"connect helper SPDX":    files.ConnectHelperSPDX.Path,
		"toolchain catalog":      files.ToolchainCatalog.Path,
		"provenance":             files.Provenance.Path,
		"notices":                files.Notices.Path,
		"release notes":          files.ReleaseNotes.Path,
		"effective policy":       files.EffectivePolicy.Path,
		"effective config":       files.EffectiveConfig.Path,
	}
	canonical := map[string]string{
		"release index":          "inputs/release-index.json",
		"release manifest":       "inputs/release-manifest.json",
		"host binary":            "inputs/loki",
		"bootstrap":              "inputs/loki-bootstrap",
		"host assets":            "inputs/host-assets.tar.gz",
		"WSL appliance":          "inputs/loki-wsl-amd64.wsl",
		"Windows frontend":       "inputs/loki-windows-amd64.exe",
		"connect helper catalog": "inputs/connect-helpers.json",
		"connect helper archive": "inputs/connect-helper-archive.zip",
		"connect helper license": "inputs/connect-helper-licenses.txt",
		"connect helper notice":  "inputs/connect-helper-NOTICE.txt",
		"connect helper SPDX":    "inputs/connect-helper.spdx.json",
		"toolchain catalog":      "inputs/toolchain-catalog.json",
		"provenance":             "inputs/provenance.bundle.json",
		"notices":                "inputs/notices.tar.gz",
		"release notes":          "inputs/release-notes.md",
		"effective policy":       "inputs/effective-policy.json",
		"effective config":       "inputs/effective-config.toml",
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
