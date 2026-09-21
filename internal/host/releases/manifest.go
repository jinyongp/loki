package releases

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	ReleaseManifestVersion = 1
	ReleaseIndexVersion    = 1
)

var (
	hostTokenPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	runtimeVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+(?:\.[0-9]+)?$`)
)

type SupportedHost struct {
	Environment  string `json:"environment"`
	Distribution string `json:"distribution"`
	Version      string `json:"version"`
	Arch         string `json:"arch"`
}

type RuntimeRequirements struct {
	DockerMin  string `json:"docker_min"`
	ComposeMin string `json:"compose_min"`
}

type ReleaseManifest struct {
	Version          int                 `json:"version"`
	Generation       Generation          `json:"generation"`
	HostBinary       TargetDescriptor    `json:"host_binary"`
	Bootstrap        TargetDescriptor    `json:"bootstrap"`
	HostAssets       TargetDescriptor    `json:"host_assets"`
	ToolchainCatalog TargetDescriptor    `json:"toolchain_catalog"`
	Provenance       TargetDescriptor    `json:"provenance"`
	Notices          TargetDescriptor    `json:"notices"`
	ReleaseNotes     TargetDescriptor    `json:"release_notes"`
	SupportedHosts   []SupportedHost     `json:"supported_hosts"`
	Runtime          RuntimeRequirements `json:"runtime"`
}

type ReleaseIndexEntry struct {
	Release      string           `json:"release"`
	ReleasedAt   time.Time        `json:"released_at"`
	GenerationID string           `json:"generation_id"`
	Manifest     TargetDescriptor `json:"manifest"`
}

type ReleaseIndex struct {
	Version int                 `json:"version"`
	Entries []ReleaseIndexEntry `json:"entries"`
}

func NewReleaseManifest(manifest ReleaseManifest) (ReleaseManifest, error) {
	if manifest.Version != ReleaseManifestVersion {
		return ReleaseManifest{}, fmt.Errorf("unsupported release manifest version %d", manifest.Version)
	}
	generation, err := NewGeneration(manifest.Generation.Spec)
	if err != nil {
		return ReleaseManifest{}, err
	}
	if manifest.Generation.ID != "" && manifest.Generation.ID != generation.ID {
		return ReleaseManifest{}, errors.New("release manifest generation identity does not match its lifecycle contract")
	}
	manifest.Generation = generation

	for name, target := range map[string]struct {
		value     TargetDescriptor
		namespace string
	}{
		"host binary":       {manifest.HostBinary, "releases"},
		"bootstrap":         {manifest.Bootstrap, "releases"},
		"host assets":       {manifest.HostAssets, "releases"},
		"toolchain catalog": {manifest.ToolchainCatalog, "toolchains"},
		"provenance":        {manifest.Provenance, "releases"},
		"notices":           {manifest.Notices, "releases"},
		"release notes":     {manifest.ReleaseNotes, "releases"},
	} {
		if err = validateTargetDescriptor(target.value, target.namespace); err != nil {
			return ReleaseManifest{}, fmt.Errorf("%s target is invalid: %w", name, err)
		}
	}
	if manifest.HostBinary.SHA256 != strings.TrimPrefix(generation.Spec.HostBinaryDigest, "sha256:") {
		return ReleaseManifest{}, errors.New("host binary target does not match the release generation digest")
	}

	hosts := append([]SupportedHost(nil), manifest.SupportedHosts...)
	if len(hosts) == 0 {
		return ReleaseManifest{}, errors.New("release manifest has no supported hosts")
	}
	for index := range hosts {
		hosts[index].Environment = strings.ToLower(strings.TrimSpace(hosts[index].Environment))
		hosts[index].Distribution = strings.ToLower(strings.TrimSpace(hosts[index].Distribution))
		hosts[index].Version = strings.TrimSpace(hosts[index].Version)
		hosts[index].Arch = strings.ToLower(strings.TrimSpace(hosts[index].Arch))
		if !hostTokenPattern.MatchString(hosts[index].Environment) ||
			!hostTokenPattern.MatchString(hosts[index].Distribution) ||
			!hostTokenPattern.MatchString(hosts[index].Version) ||
			!hostTokenPattern.MatchString(hosts[index].Arch) {
			return ReleaseManifest{}, errors.New("release manifest contains an invalid supported host")
		}
	}
	slices.SortFunc(hosts, func(left, right SupportedHost) int {
		if value := strings.Compare(left.Environment, right.Environment); value != 0 {
			return value
		}
		if value := strings.Compare(left.Distribution, right.Distribution); value != 0 {
			return value
		}
		if value := strings.Compare(left.Version, right.Version); value != 0 {
			return value
		}
		return strings.Compare(left.Arch, right.Arch)
	})
	for index := 1; index < len(hosts); index++ {
		if hosts[index] == hosts[index-1] {
			return ReleaseManifest{}, errors.New("release manifest supported hosts must be unique")
		}
	}
	manifest.SupportedHosts = hosts

	manifest.Runtime.DockerMin = strings.TrimSpace(manifest.Runtime.DockerMin)
	manifest.Runtime.ComposeMin = strings.TrimSpace(manifest.Runtime.ComposeMin)
	if !runtimeVersionPattern.MatchString(manifest.Runtime.DockerMin) ||
		!runtimeVersionPattern.MatchString(manifest.Runtime.ComposeMin) {
		return ReleaseManifest{}, errors.New("release manifest runtime requirements are invalid")
	}
	return manifest, nil
}

func LoadReleaseManifest(raw []byte) (ReleaseManifest, error) {
	var manifest ReleaseManifest
	if err := decodeStrictJSON(raw, &manifest, "release manifest"); err != nil {
		return ReleaseManifest{}, err
	}
	return NewReleaseManifest(manifest)
}

func EncodeReleaseManifest(manifest ReleaseManifest) ([]byte, error) {
	normalized, err := NewReleaseManifest(manifest)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func NewReleaseIndex(entries []ReleaseIndexEntry) (ReleaseIndex, error) {
	index := ReleaseIndex{Version: ReleaseIndexVersion, Entries: append([]ReleaseIndexEntry(nil), entries...)}
	slices.SortFunc(index.Entries, func(left, right ReleaseIndexEntry) int {
		if left.ReleasedAt.Before(right.ReleasedAt) {
			return -1
		}
		if left.ReleasedAt.After(right.ReleasedAt) {
			return 1
		}
		return strings.Compare(left.Release, right.Release)
	})
	if err := index.Validate(); err != nil {
		return ReleaseIndex{}, err
	}
	return index, nil
}

func LoadReleaseIndex(raw []byte) (ReleaseIndex, error) {
	var index ReleaseIndex
	if err := decodeStrictJSON(raw, &index, "release index"); err != nil {
		return ReleaseIndex{}, err
	}
	if index.Version != ReleaseIndexVersion {
		return ReleaseIndex{}, fmt.Errorf("unsupported release index version %d", index.Version)
	}
	normalized, err := NewReleaseIndex(index.Entries)
	if err != nil {
		return ReleaseIndex{}, err
	}
	return normalized, nil
}

func (index ReleaseIndex) Validate() error {
	if index.Version != ReleaseIndexVersion {
		return fmt.Errorf("unsupported release index version %d", index.Version)
	}
	if len(index.Entries) == 0 {
		return errors.New("release index has no entries")
	}
	seenRelease := map[string]bool{}
	seenGeneration := map[string]bool{}
	var previous *ReleaseIndexEntry
	for position := range index.Entries {
		entry := &index.Entries[position]
		entry.Release = strings.TrimSpace(entry.Release)
		entry.ReleasedAt = entry.ReleasedAt.UTC().Truncate(time.Second)
		if !releaseNamePattern.MatchString(entry.Release) || entry.ReleasedAt.IsZero() || !digestPattern.MatchString(entry.GenerationID) {
			return errors.New("release index entry identity is invalid")
		}
		if seenRelease[entry.Release] || seenGeneration[entry.GenerationID] {
			return errors.New("release index entries must have unique release and generation identities")
		}
		seenRelease[entry.Release] = true
		seenGeneration[entry.GenerationID] = true
		if err := validateTargetDescriptor(entry.Manifest, "releases"); err != nil {
			return fmt.Errorf("release index manifest target is invalid: %w", err)
		}
		if entry.Manifest.Path != "releases/manifests/"+entry.Release+".json" {
			return errors.New("release index manifest target path does not match the release identity")
		}
		if previous != nil {
			if entry.ReleasedAt.Before(previous.ReleasedAt) ||
				entry.ReleasedAt.Equal(previous.ReleasedAt) && entry.Release <= previous.Release {
				return errors.New("release index entries must be sorted by release timestamp and identity")
			}
		}
		copy := *entry
		previous = &copy
	}
	return nil
}

func IndexEntryForManifest(manifest ReleaseManifest, descriptor TargetDescriptor) (ReleaseIndexEntry, error) {
	normalized, err := NewReleaseManifest(manifest)
	if err != nil {
		return ReleaseIndexEntry{}, err
	}
	if err = validateTargetDescriptor(descriptor, "releases"); err != nil {
		return ReleaseIndexEntry{}, err
	}
	expectedPath := "releases/manifests/" + normalized.Generation.Spec.Version + ".json"
	if descriptor.Path != expectedPath {
		return ReleaseIndexEntry{}, errors.New("release manifest target path does not match the release identity")
	}
	raw, err := EncodeReleaseManifest(normalized)
	if err != nil {
		return ReleaseIndexEntry{}, err
	}
	if err = descriptor.VerifyBytes(raw); err != nil {
		return ReleaseIndexEntry{}, fmt.Errorf("release manifest target identity does not match canonical manifest bytes: %w", err)
	}
	return ReleaseIndexEntry{
		Release:      normalized.Generation.Spec.Version,
		ReleasedAt:   normalized.Generation.Spec.ReleasedAt,
		GenerationID: normalized.Generation.ID,
		Manifest:     descriptor,
	}, nil
}

func (entry ReleaseIndexEntry) VerifyManifest(raw []byte) (ReleaseManifest, error) {
	if err := validateTargetDescriptor(entry.Manifest, "releases"); err != nil {
		return ReleaseManifest{}, err
	}
	if err := entry.Manifest.VerifyBytes(raw); err != nil {
		return ReleaseManifest{}, fmt.Errorf("release manifest target verification failed: %w", err)
	}
	manifest, err := LoadReleaseManifest(raw)
	if err != nil {
		return ReleaseManifest{}, err
	}
	if entry.Release != manifest.Generation.Spec.Version ||
		!entry.ReleasedAt.UTC().Truncate(time.Second).Equal(manifest.Generation.Spec.ReleasedAt) ||
		entry.GenerationID != manifest.Generation.ID {
		return ReleaseManifest{}, errors.New("release index entry does not match the verified manifest identity")
	}
	expectedPath := "releases/manifests/" + entry.Release + ".json"
	if entry.Manifest.Path != expectedPath {
		return ReleaseManifest{}, errors.New("release index manifest target path does not match the release identity")
	}
	return manifest, nil
}

func validateTargetDescriptor(descriptor TargetDescriptor, namespace string) error {
	relative := strings.TrimPrefix(descriptor.Path, namespace+"/")
	expected, err := namespacedTargetPath(namespace, relative)
	if err != nil || expected != descriptor.Path {
		return errors.New("target path is outside its authenticated namespace")
	}
	if descriptor.Length <= 0 || descriptor.Length > maxTargetBytes || !sha256Pattern.MatchString(descriptor.SHA256) {
		return errors.New("target content identity is invalid")
	}
	return nil
}

func decodeStrictJSON(raw []byte, target any, name string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s contains trailing data", name)
	}
	return nil
}
