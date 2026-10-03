package toolchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	goversion "go/version"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var goPrereleasePattern = regexp.MustCompile(`^[1-9][0-9]*\.[0-9]+(?:beta|rc)[1-9][0-9]*$`)

type GoVersionScheme struct{}

func (GoVersionScheme) ParseProjectSelector(raw string) (Selector, error) {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "*", "go", "latest":
		return NewSelector(SelectorFloating, "*")
	}
	raw = strings.TrimPrefix(raw, "go")
	if goPrereleasePattern.MatchString(raw) {
		version, err := (GoVersionScheme{}).NormalizeVersion(raw)
		if err != nil {
			return Selector{}, err
		}
		return NewSelector(SelectorExact, version)
	}
	parts, err := parseNumericVersionParts(raw, 2, 3)
	if err != nil {
		return Selector{}, fmt.Errorf("invalid Go selector %q", raw)
	}
	kind := SelectorPartial
	if len(parts) == 3 {
		kind = SelectorExact
	}
	return NewSelector(kind, joinVersionParts(parts))
}

func (GoVersionScheme) NormalizeVersion(raw string) (string, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "go"))
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00/\\") {
		return "", errors.New("Go version is invalid")
	}
	if goPrereleasePattern.MatchString(raw) {
		if !goversion.IsValid("go" + raw) {
			return "", fmt.Errorf("invalid Go version %q", raw)
		}
		return raw, nil
	}
	parts, err := parseNumericVersionParts(raw, 2, 3)
	if err != nil {
		return "", fmt.Errorf("invalid Go version %q", raw)
	}
	if len(parts) == 2 && (parts[0] > 1 || parts[0] == 1 && parts[1] >= 21) {
		return "", fmt.Errorf("Go release version %q must include a patch component", raw)
	}
	normalized := joinVersionParts(parts)
	if !goversion.IsValid("go" + normalized) {
		return "", fmt.Errorf("invalid Go version %q", raw)
	}
	return normalized, nil
}

func (GoVersionScheme) Match(selector Selector, version string) (bool, error) {
	version, err := (GoVersionScheme{}).NormalizeVersion(version)
	if err != nil {
		return false, err
	}
	switch selector.Kind {
	case SelectorFloating:
		return true, nil
	case SelectorExact:
		exact, err := (GoVersionScheme{}).NormalizeVersion(selector.Value)
		return version == exact, err
	case SelectorPartial:
		parts, err := parseNumericVersionParts(selector.Value, 2, 2)
		if err != nil {
			return false, err
		}
		prefix := joinVersionParts(parts)
		return strings.HasPrefix(version, prefix+".") || version == prefix, nil
	default:
		return false, errors.New("invalid Go selector kind")
	}
}

func (GoVersionScheme) Compare(left, right string) (int, error) {
	left, err := (GoVersionScheme{}).NormalizeVersion(left)
	if err != nil {
		return 0, err
	}
	right, err = (GoVersionScheme{}).NormalizeVersion(right)
	if err != nil {
		return 0, err
	}
	return goversion.Compare("go"+left, "go"+right), nil
}

func normalizeGoDirectiveVersion(raw string) (string, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "go"))
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00/\\") || !goversion.IsValid("go"+raw) {
		return "", fmt.Errorf("invalid Go directive version %q", raw)
	}
	return raw, nil
}

type GoProjectRequest struct {
	Minimum   string
	Toolchain string
}

type normalizedGoProjectRequest struct {
	Minimum   string
	Toolchain string
}

func normalizeGoProjectRequest(request GoProjectRequest) (normalizedGoProjectRequest, error) {
	minimum, err := normalizeGoDirectiveVersion(request.Minimum)
	if err != nil {
		return normalizedGoProjectRequest{}, err
	}
	toolchain := strings.TrimSpace(request.Toolchain)
	if toolchain == "" || toolchain == "default" {
		return normalizedGoProjectRequest{Minimum: minimum, Toolchain: toolchain}, nil
	}
	if !strings.HasPrefix(toolchain, "go") {
		return normalizedGoProjectRequest{}, errors.New("Go toolchain directive must use an official goV toolchain name or default")
	}
	version, err := (GoVersionScheme{}).NormalizeVersion(strings.TrimPrefix(toolchain, "go"))
	if err != nil || "go"+version != toolchain {
		return normalizedGoProjectRequest{}, errors.New("Go toolchain directive must use a canonical official goV toolchain name")
	}
	if goversion.Compare("go"+version, "go"+minimum) < 0 {
		return normalizedGoProjectRequest{}, errors.New("Go toolchain directive cannot be older than the go directive")
	}
	return normalizedGoProjectRequest{Minimum: minimum, Toolchain: toolchain}, nil
}

type GoRelease struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

func (r GoRelease) Validate() error {
	version, err := (GoVersionScheme{}).NormalizeVersion(r.Version)
	if err != nil || version != r.Version {
		return errors.New("Go release version must be canonical")
	}
	if !sha256Text.MatchString(r.SHA256) {
		return errors.New("Go release checksum must be a lowercase sha256 digest")
	}
	parsed, err := url.Parse(r.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "go.dev" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Go release URL must use the canonical go.dev HTTPS origin")
	}
	expectedPath := "/dl/" + r.Filename()
	if parsed.Path != expectedPath {
		return fmt.Errorf("Go release URL path must be %s", expectedPath)
	}
	return nil
}

func (r GoRelease) Filename() string {
	return "go" + strings.TrimSpace(r.Version) + ".linux-amd64.tar.gz"
}

func (r GoRelease) GenerationID() string {
	sum := sha256.Sum256([]byte("loki-go-toolchain-generation:v1\n" + r.Version + "\n" + r.URL + "\n" + r.SHA256 + "\n"))
	return hex.EncodeToString(sum[:])
}

type GoPlan struct {
	Request      GoProjectRequest
	Resolution   VersionResolution
	Release      GoRelease
	GenerationID string
}

type GoProvider struct {
	Store GenerationStore
}

func (p GoProvider) Resolve(request GoProjectRequest, releases []GoRelease, update bool) (GoPlan, error) {
	normalized, err := normalizeGoProjectRequest(request)
	if err != nil {
		return GoPlan{}, err
	}
	if len(releases) == 0 {
		return GoPlan{}, errors.New("Go trusted release set is empty")
	}
	permitted := make([]string, 0, len(releases))
	installed := make([]string, 0, len(releases))
	byVersion := make(map[string]GoRelease, len(releases))
	for _, release := range releases {
		if err = release.Validate(); err != nil {
			return GoPlan{}, err
		}
		if goversion.Compare("go"+release.Version, "go"+normalized.Minimum) < 0 {
			continue
		}
		if normalized.Toolchain != "" && normalized.Toolchain != "default" &&
			goversion.Compare("go"+release.Version, normalized.Toolchain) < 0 {
			continue
		}
		if previous, exists := byVersion[release.Version]; exists {
			if previous != release {
				return GoPlan{}, fmt.Errorf("Go trusted release %s has conflicting artifact identities", release.Version)
			}
			continue
		}
		byVersion[release.Version] = release
		permitted = append(permitted, release.Version)
		if _, lookupErr := p.Store.Lookup(release.GenerationID()); lookupErr == nil {
			installed = append(installed, release.Version)
		} else if !errors.Is(lookupErr, os.ErrNotExist) {
			return GoPlan{}, lookupErr
		}
	}
	if len(permitted) == 0 {
		return GoPlan{}, errors.New("Go project requirement has no administrator-permitted release")
	}
	selector, err := NewSelector(SelectorFloating, "*")
	if err != nil {
		return GoPlan{}, err
	}
	resolution, err := ResolveVersion(selector, installed, permitted, update, GoVersionScheme{})
	if err != nil {
		return GoPlan{}, err
	}
	release, ok := byVersion[resolution.Version]
	if !ok {
		return GoPlan{}, errors.New("Go resolution escaped trusted release set")
	}
	return GoPlan{
		Request:    GoProjectRequest{Minimum: normalized.Minimum, Toolchain: normalized.Toolchain},
		Resolution: resolution, Release: release, GenerationID: release.GenerationID(),
	}, nil
}

func (p GoProvider) Provision(ctx context.Context, plan GoPlan, source string) (Generation, error) {
	normalized, err := normalizeGoProjectRequest(plan.Request)
	if err != nil {
		return Generation{}, err
	}
	if err = plan.Release.Validate(); err != nil {
		return Generation{}, err
	}
	if goversion.Compare("go"+plan.Release.Version, "go"+normalized.Minimum) < 0 ||
		(normalized.Toolchain != "" && normalized.Toolchain != "default" &&
			goversion.Compare("go"+plan.Release.Version, normalized.Toolchain) < 0) ||
		plan.GenerationID != plan.Release.GenerationID() || plan.Resolution.Version != plan.Release.Version {
		return Generation{}, errors.New("Go install plan identity is inconsistent")
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return Generation{}, errors.New("Go artifact source must be a clean absolute path")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return Generation{}, errors.New("Go artifact source must be a regular file")
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
		return Generation{}, errors.New("Go artifact checksum mismatch")
	}
	return p.Store.Provision(ctx, plan.GenerationID, func(ctx context.Context, root string) error {
		artifact := Artifact{
			Name: "go", Version: plan.Release.Version, Filename: plan.Release.Filename(),
			URL: plan.Release.URL, SHA256: plan.Release.SHA256, Format: "tar.gz",
			InstallPath: "/opt/loki/toolchain/go/" + plan.Release.Version, StripComponents: 1,
			Links: map[string]string{"go": "bin/go", "gofmt": "bin/gofmt"},
		}
		if err := installArtifact(ctx, root, source, artifact); err != nil {
			return err
		}
		base := filepath.Join(root, "opt", "loki", "toolchain", "go", plan.Release.Version, "bin")
		for _, name := range []string{"go", "gofmt"} {
			info, statErr := os.Stat(filepath.Join(base, name))
			if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return fmt.Errorf("Go artifact is missing executable %s", name)
			}
		}
		return nil
	})
}

func (p GoPlan) Executables(generation Generation) (map[string]string, error) {
	if generation.ID != p.GenerationID || p.GenerationID != p.Release.GenerationID() {
		return nil, errors.New("Go generation does not match install plan")
	}
	base := filepath.Join(generation.Root, "opt", "loki", "toolchain", "go", p.Release.Version, "bin")
	result := map[string]string{
		"go":    filepath.Join(base, "go"),
		"gofmt": filepath.Join(base, "gofmt"),
	}
	for name, path := range result {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return nil, fmt.Errorf("Go executable %s is unavailable", name)
		}
	}
	return result, nil
}
