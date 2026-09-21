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

type PnpmVersionScheme struct{}

func (PnpmVersionScheme) ParseProjectSelector(raw string) (Selector, error) {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "*", "pnpm", "latest":
		return NewSelector(SelectorFloating, "*")
	}
	raw = strings.TrimPrefix(raw, "v")
	parts, err := parseNumericVersionParts(raw, 1, 3)
	if err != nil {
		return Selector{}, fmt.Errorf("invalid pnpm selector %q", raw)
	}
	kind := SelectorPartial
	if len(parts) == 3 {
		kind = SelectorExact
	}
	return NewSelector(kind, joinVersionParts(parts))
}

func (PnpmVersionScheme) NormalizeVersion(raw string) (string, error) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	parts, err := parseNumericVersionParts(raw, 3, 3)
	if err != nil {
		return "", fmt.Errorf("invalid pnpm version %q", raw)
	}
	return joinVersionParts(parts), nil
}

func (PnpmVersionScheme) Match(selector Selector, version string) (bool, error) {
	version, err := (PnpmVersionScheme{}).NormalizeVersion(version)
	if err != nil {
		return false, err
	}
	versionParts, _ := parseNumericVersionParts(version, 3, 3)
	switch selector.Kind {
	case SelectorFloating:
		return true, nil
	case SelectorExact:
		exact, err := (PnpmVersionScheme{}).NormalizeVersion(selector.Value)
		return version == exact, err
	case SelectorPartial:
		parts, err := parseNumericVersionParts(selector.Value, 1, 2)
		if err != nil {
			return false, err
		}
		for index, part := range parts {
			if versionParts[index] != part {
				return false, nil
			}
		}
		return true, nil
	default:
		return false, errors.New("invalid pnpm selector kind")
	}
}

func (PnpmVersionScheme) Compare(left, right string) (int, error) {
	leftText, err := (PnpmVersionScheme{}).NormalizeVersion(left)
	if err != nil {
		return 0, err
	}
	rightText, err := (PnpmVersionScheme{}).NormalizeVersion(right)
	if err != nil {
		return 0, err
	}
	leftParts, _ := parseNumericVersionParts(leftText, 3, 3)
	rightParts, _ := parseNumericVersionParts(rightText, 3, 3)
	for index := range leftParts {
		switch {
		case leftParts[index] < rightParts[index]:
			return -1, nil
		case leftParts[index] > rightParts[index]:
			return 1, nil
		}
	}
	return 0, nil
}

func ParsePnpmPackageManager(raw string) (Selector, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "pnpm@") {
		return Selector{}, errors.New("packageManager must select pnpm with a pnpm@ version declaration")
	}
	value := strings.TrimPrefix(raw, "pnpm@")
	version, integrity, hasIntegrity := strings.Cut(value, "+")
	if hasIntegrity && (integrity == "" || len(integrity) > 1024 || strings.ContainsAny(integrity, "\r\n\x00")) {
		return Selector{}, errors.New("pnpm packageManager integrity suffix is invalid")
	}
	return ParseProjectSelector(version, PnpmVersionScheme{})
}

type PnpmRelease struct {
	Version      string `json:"version"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	NodeMajorMin uint64 `json:"node_major_min"`
	NodeMajorMax uint64 `json:"node_major_max,omitempty"`
}

func (r PnpmRelease) Validate() error {
	version, err := (PnpmVersionScheme{}).NormalizeVersion(r.Version)
	if err != nil || version != r.Version {
		return errors.New("pnpm release version must be canonical")
	}
	if !sha256Text.MatchString(r.SHA256) {
		return errors.New("pnpm release checksum must be a lowercase sha256 digest")
	}
	if r.NodeMajorMin == 0 || r.NodeMajorMax != 0 && r.NodeMajorMax < r.NodeMajorMin {
		return errors.New("pnpm Node.js compatibility range is invalid")
	}
	parsed, err := url.Parse(r.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("pnpm release URL must use the canonical GitHub HTTPS origin")
	}
	expectedPath := "/pnpm/pnpm/releases/download/v" + version + "/" + r.Filename()
	if parsed.Path != expectedPath {
		return fmt.Errorf("pnpm release URL path must be %s", expectedPath)
	}
	return nil
}

func (r PnpmRelease) Filename() string {
	return "pnpm-linux-x64.tar.gz"
}

func (r PnpmRelease) GenerationID() string {
	sum := sha256.Sum256([]byte("loki-pnpm-generation:v1\n" + r.Version + "\n" + r.URL + "\n" + r.SHA256 + "\n"))
	return hex.EncodeToString(sum[:])
}

func (r PnpmRelease) SupportsNode(version string) (bool, error) {
	normalized, err := (NodeVersionScheme{}).NormalizeVersion(version)
	if err != nil {
		return false, err
	}
	parts, _ := parseNumericVersionParts(normalized, 3, 3)
	major := parts[0]
	if major < r.NodeMajorMin {
		return false, nil
	}
	if r.NodeMajorMax != 0 && major > r.NodeMajorMax {
		return false, nil
	}
	return true, nil
}

type PnpmPlan struct {
	Resolution       VersionResolution
	Release          PnpmRelease
	GenerationID     string
	NodeVersion      string
	NodeGenerationID string
}

type PnpmProvider struct {
	Store GenerationStore
}

func (p PnpmProvider) Resolve(packageManager string, releases []PnpmRelease, node NodePlan, update bool) (PnpmPlan, error) {
	selector, err := ParsePnpmPackageManager(packageManager)
	if err != nil {
		return PnpmPlan{}, err
	}
	if err = node.Release.Validate(); err != nil ||
		node.GenerationID == "" || node.GenerationID != node.Release.GenerationID() ||
		node.Resolution.Version == "" || node.Resolution.Version != node.Release.Version {
		return PnpmPlan{}, errors.New("pnpm resolution requires a valid selected Node.js plan")
	}
	if len(releases) == 0 {
		return PnpmPlan{}, errors.New("pnpm trusted release set is empty")
	}
	versions := make([]string, 0, len(releases))
	byVersion := make(map[string]PnpmRelease, len(releases))
	installed := make([]string, 0, len(releases))
	for _, release := range releases {
		if err = release.Validate(); err != nil {
			return PnpmPlan{}, err
		}
		compatible, compatibilityErr := release.SupportsNode(node.Release.Version)
		if compatibilityErr != nil {
			return PnpmPlan{}, compatibilityErr
		}
		if !compatible {
			continue
		}
		if previous, exists := byVersion[release.Version]; exists {
			if previous != release {
				return PnpmPlan{}, fmt.Errorf("pnpm trusted release %s has conflicting artifact identities", release.Version)
			}
			continue
		}
		byVersion[release.Version] = release
		versions = append(versions, release.Version)
		if _, lookupErr := p.Store.Lookup(release.GenerationID()); lookupErr == nil {
			installed = append(installed, release.Version)
		} else if !errors.Is(lookupErr, os.ErrNotExist) {
			return PnpmPlan{}, lookupErr
		}
	}
	if len(versions) == 0 {
		return PnpmPlan{}, errors.New("pnpm has no administrator-permitted release compatible with selected Node.js")
	}
	resolution, err := ResolveVersion(selector, installed, versions, update, PnpmVersionScheme{})
	if err != nil {
		return PnpmPlan{}, err
	}
	release, ok := byVersion[resolution.Version]
	if !ok {
		return PnpmPlan{}, errors.New("pnpm resolution escaped trusted release set")
	}
	return PnpmPlan{
		Resolution: resolution, Release: release, GenerationID: release.GenerationID(),
		NodeVersion: node.Release.Version, NodeGenerationID: node.GenerationID,
	}, nil
}

func (p PnpmProvider) Provision(ctx context.Context, plan PnpmPlan, source string) (Generation, error) {
	if err := plan.Release.Validate(); err != nil {
		return Generation{}, err
	}
	if plan.GenerationID != plan.Release.GenerationID() || plan.Resolution.Version != plan.Release.Version ||
		plan.NodeVersion == "" || plan.NodeGenerationID == "" {
		return Generation{}, errors.New("pnpm install plan identity is inconsistent")
	}
	compatible, err := plan.Release.SupportsNode(plan.NodeVersion)
	if err != nil || !compatible {
		return Generation{}, errors.New("pnpm install plan is incompatible with selected Node.js")
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return Generation{}, errors.New("pnpm artifact source must be a clean absolute path")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return Generation{}, err
	}
	if !info.Mode().IsRegular() {
		return Generation{}, errors.New("pnpm artifact source must be a regular file")
	}
	releaseArtifact, err := p.Store.LockArtifact(ctx, plan.Release.SHA256)
	if err != nil {
		return Generation{}, err
	}
	defer releaseArtifact()
	digest, err := fileDigest(source)
	if err != nil {
		return Generation{}, err
	}
	if digest != plan.Release.SHA256 {
		return Generation{}, errors.New("pnpm artifact checksum mismatch")
	}
	return p.Store.Provision(ctx, plan.GenerationID, func(ctx context.Context, root string) error {
		artifact := Artifact{
			Name: "pnpm", Version: plan.Release.Version, Filename: plan.Release.Filename(),
			URL: plan.Release.URL, SHA256: plan.Release.SHA256, Format: "tar.gz",
			InstallPath:     "/opt/loki/toolchain/pnpm/" + plan.Release.Version,
			StripComponents: 0,
			Links:           map[string]string{"pnpm": "pnpm"},
		}
		if err := installArtifact(ctx, root, source, artifact); err != nil {
			return err
		}
		path := filepath.Join(root, "opt", "loki", "toolchain", "pnpm", plan.Release.Version, "pnpm")
		executable, statErr := os.Stat(path)
		if statErr != nil || !executable.Mode().IsRegular() || executable.Mode().Perm()&0111 == 0 {
			return errors.New("pnpm artifact is missing native pnpm executable")
		}
		return nil
	})
}

func (p PnpmPlan) Executable(generation Generation) (string, error) {
	if generation.ID != p.GenerationID || p.GenerationID != p.Release.GenerationID() {
		return "", errors.New("pnpm generation does not match install plan")
	}
	path := filepath.Join(generation.Root, "opt", "loki", "toolchain", "pnpm", p.Release.Version, "pnpm")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", errors.New("pnpm executable is unavailable")
	}
	return path, nil
}
