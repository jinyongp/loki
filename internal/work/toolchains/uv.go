package toolchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type UVVersionScheme struct{}

func (UVVersionScheme) ParseProjectSelector(raw string) (Selector, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "v"))
	switch strings.ToLower(raw) {
	case "*", "uv", "latest":
		return NewSelector(SelectorFloating, "*")
	}
	parts, err := parseNumericVersionParts(raw, 1, 3)
	if err != nil {
		return Selector{}, fmt.Errorf("invalid uv selector %q", raw)
	}
	kind := SelectorPartial
	if len(parts) == 3 {
		kind = SelectorExact
	}
	return NewSelector(kind, joinVersionParts(parts))
}

func (UVVersionScheme) NormalizeVersion(raw string) (string, error) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	parts, err := parseNumericVersionParts(raw, 3, 3)
	if err != nil {
		return "", fmt.Errorf("invalid uv version %q", raw)
	}
	return joinVersionParts(parts), nil
}

func (UVVersionScheme) Match(selector Selector, version string) (bool, error) {
	version, err := (UVVersionScheme{}).NormalizeVersion(version)
	if err != nil {
		return false, err
	}
	switch selector.Kind {
	case SelectorFloating:
		return true, nil
	case SelectorExact:
		exact, err := (UVVersionScheme{}).NormalizeVersion(selector.Value)
		return version == exact, err
	case SelectorPartial:
		parts, err := parseNumericVersionParts(selector.Value, 1, 2)
		if err != nil {
			return false, err
		}
		versionParts, _ := parseNumericVersionParts(version, 3, 3)
		for index, part := range parts {
			if versionParts[index] != part {
				return false, nil
			}
		}
		return true, nil
	default:
		return false, errors.New("invalid uv selector kind")
	}
}

func (UVVersionScheme) Compare(left, right string) (int, error) {
	leftParts, err := parseUVVersion(left)
	if err != nil {
		return 0, err
	}
	rightParts, err := parseUVVersion(right)
	if err != nil {
		return 0, err
	}
	for index := range leftParts {
		if leftParts[index] < rightParts[index] {
			return -1, nil
		}
		if leftParts[index] > rightParts[index] {
			return 1, nil
		}
	}
	return 0, nil
}

func parseUVVersion(raw string) ([3]uint64, error) {
	var result [3]uint64
	normalized, err := (UVVersionScheme{}).NormalizeVersion(raw)
	if err != nil {
		return result, err
	}
	parts, _ := parseNumericVersionParts(normalized, 3, 3)
	copy(result[:], parts)
	return result, nil
}

type UVRelease struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

func (r UVRelease) Filename() string { return "uv-x86_64-unknown-linux-gnu.tar.gz" }

func (r UVRelease) Validate() error {
	version, err := (UVVersionScheme{}).NormalizeVersion(r.Version)
	if err != nil || version != r.Version {
		return errors.New("uv release version must be canonical")
	}
	if !sha256Text.MatchString(r.SHA256) {
		return errors.New("uv release checksum must be a lowercase sha256 digest")
	}
	parsed, err := url.Parse(r.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("uv release URL must use the canonical GitHub HTTPS origin")
	}
	expected := "/astral-sh/uv/releases/download/" + r.Version + "/" + r.Filename()
	if parsed.Path != expected {
		return fmt.Errorf("uv release URL path must be %s", expected)
	}
	return nil
}

func (r UVRelease) GenerationID() string {
	sum := sha256.Sum256([]byte("loki-uv-generation:v1\n" + r.Version + "\n" + r.URL + "\n" + r.SHA256 + "\n"))
	return hex.EncodeToString(sum[:])
}

type UVPlan struct {
	Resolution   VersionResolution
	Release      UVRelease
	GenerationID string
}

type UVProvider struct{ Store GenerationStore }

func (p UVProvider) Resolve(raw string, releases []UVRelease, update bool) (UVPlan, error) {
	selector, err := ParseProjectSelector(raw, UVVersionScheme{})
	if err != nil {
		return UVPlan{}, err
	}
	if len(releases) == 0 {
		return UVPlan{}, errors.New("uv trusted release set is empty")
	}
	permitted := make([]string, 0, len(releases))
	installed := make([]string, 0, len(releases))
	byVersion := make(map[string]UVRelease, len(releases))
	for _, release := range releases {
		if err := release.Validate(); err != nil {
			return UVPlan{}, err
		}
		if previous, exists := byVersion[release.Version]; exists {
			if previous != release {
				return UVPlan{}, fmt.Errorf("uv trusted release %s has conflicting artifact identities", release.Version)
			}
			continue
		}
		byVersion[release.Version] = release
		permitted = append(permitted, release.Version)
		if _, err := p.Store.Lookup(release.GenerationID()); err == nil {
			installed = append(installed, release.Version)
		} else if !errors.Is(err, os.ErrNotExist) {
			return UVPlan{}, err
		}
	}
	resolution, err := ResolveVersion(selector, installed, permitted, update, UVVersionScheme{})
	if err != nil {
		return UVPlan{}, err
	}
	release, ok := byVersion[resolution.Version]
	if !ok {
		return UVPlan{}, errors.New("uv resolution escaped trusted release set")
	}
	return UVPlan{Resolution: resolution, Release: release, GenerationID: release.GenerationID()}, nil
}

func (p UVProvider) Provision(ctx context.Context, plan UVPlan, source string) (Generation, error) {
	if err := plan.Release.Validate(); err != nil {
		return Generation{}, err
	}
	if plan.GenerationID != plan.Release.GenerationID() || plan.Resolution.Version != plan.Release.Version {
		return Generation{}, errors.New("uv install plan identity is inconsistent")
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return Generation{}, errors.New("uv artifact source must be a clean absolute path")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return Generation{}, errors.New("uv artifact source must be a regular file")
	}
	unlock, err := p.Store.LockArtifact(ctx, plan.Release.SHA256)
	if err != nil {
		return Generation{}, err
	}
	defer unlock()
	digest, err := fileDigest(source)
	if err != nil {
		return Generation{}, err
	}
	if digest != plan.Release.SHA256 {
		return Generation{}, errors.New("uv artifact checksum mismatch")
	}
	return p.Store.Provision(ctx, plan.GenerationID, func(ctx context.Context, root string) error {
		artifact := Artifact{
			Name: "uv", Version: plan.Release.Version, Filename: plan.Release.Filename(),
			URL: plan.Release.URL, SHA256: plan.Release.SHA256, Format: "tar.gz",
			InstallPath: "/opt/loki/toolchain/uv/" + plan.Release.Version, StripComponents: 1,
			Links: map[string]string{"uv": "uv", "uvx": "uvx"},
		}
		if err := installArtifact(ctx, root, source, artifact); err != nil {
			return err
		}
		base := filepath.Join(root, "opt", "loki", "toolchain", "uv", plan.Release.Version)
		for _, name := range []string{"uv", "uvx"} {
			info, err := os.Stat(filepath.Join(base, name))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return fmt.Errorf("uv artifact is missing executable %s", name)
			}
		}
		return nil
	})
}
