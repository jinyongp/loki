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
	"strconv"
	"strings"
)

type NodeVersionScheme struct{}

func (NodeVersionScheme) ParseProjectSelector(raw string) (Selector, error) {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "*", "node", "latest":
		return NewSelector(SelectorFloating, "*")
	}
	raw = strings.TrimPrefix(raw, "v")
	parts, err := parseNumericVersionParts(raw, 1, 3)
	if err != nil {
		return Selector{}, fmt.Errorf("invalid Node.js selector %q", raw)
	}
	kind := SelectorPartial
	if len(parts) == 3 {
		kind = SelectorExact
	}
	return NewSelector(kind, joinVersionParts(parts))
}

func (NodeVersionScheme) NormalizeVersion(raw string) (string, error) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	parts, err := parseNumericVersionParts(raw, 3, 3)
	if err != nil {
		return "", fmt.Errorf("invalid Node.js version %q", raw)
	}
	return joinVersionParts(parts), nil
}

func (NodeVersionScheme) Match(selector Selector, version string) (bool, error) {
	version, err := (NodeVersionScheme{}).NormalizeVersion(version)
	if err != nil {
		return false, err
	}
	versionParts, _ := parseNumericVersionParts(version, 3, 3)
	switch selector.Kind {
	case SelectorFloating:
		return true, nil
	case SelectorExact:
		exact, err := (NodeVersionScheme{}).NormalizeVersion(selector.Value)
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
		return false, errors.New("invalid Node.js selector kind")
	}
}

func (NodeVersionScheme) Compare(left, right string) (int, error) {
	leftText, err := (NodeVersionScheme{}).NormalizeVersion(left)
	if err != nil {
		return 0, err
	}
	rightText, err := (NodeVersionScheme{}).NormalizeVersion(right)
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

func parseNumericVersionParts(raw string, minimum, maximum int) ([]uint64, error) {
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00+-") {
		return nil, errors.New("version contains unsupported syntax")
	}
	text := strings.Split(raw, ".")
	if len(text) < minimum || len(text) > maximum {
		return nil, errors.New("version has an unsupported number of components")
	}
	parts := make([]uint64, len(text))
	for index, part := range text {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return nil, errors.New("version component is empty or has a leading zero")
		}
		value, err := strconv.ParseUint(part, 10, 31)
		if err != nil {
			return nil, errors.New("version component is not a supported integer")
		}
		parts[index] = value
	}
	return parts, nil
}

func joinVersionParts(parts []uint64) string {
	text := make([]string, len(parts))
	for index, part := range parts {
		text[index] = strconv.FormatUint(part, 10)
	}
	return strings.Join(text, ".")
}

type NodeRelease struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

func (r NodeRelease) Validate() error {
	version, err := (NodeVersionScheme{}).NormalizeVersion(r.Version)
	if err != nil || version != r.Version {
		return errors.New("Node.js release version must be canonical")
	}
	if !sha256Text.MatchString(r.SHA256) {
		return errors.New("Node.js release checksum must be a lowercase sha256 digest")
	}
	parsed, err := url.Parse(r.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "nodejs.org" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Node.js release URL must use the canonical nodejs.org HTTPS origin")
	}
	filename := r.Filename()
	expectedPath := "/download/release/v" + version + "/" + filename
	if parsed.Path != expectedPath {
		return fmt.Errorf("Node.js release URL path must be %s", expectedPath)
	}
	return nil
}

func (r NodeRelease) Filename() string {
	version := strings.TrimSpace(r.Version)
	return "node-v" + version + "-linux-x64.tar.xz"
}

func (r NodeRelease) GenerationID() string {
	sum := sha256.Sum256([]byte("loki-node-generation:v1\n" + r.Version + "\n" + r.URL + "\n" + r.SHA256 + "\n"))
	return hex.EncodeToString(sum[:])
}

type NodePlan struct {
	Resolution   VersionResolution
	Release      NodeRelease
	GenerationID string
}

type NodeProvider struct {
	Store GenerationStore
}

func (p NodeProvider) Resolve(rawSelector string, releases []NodeRelease, update bool) (NodePlan, error) {
	selector, err := ParseProjectSelector(rawSelector, NodeVersionScheme{})
	if err != nil {
		return NodePlan{}, err
	}
	if len(releases) == 0 {
		return NodePlan{}, errors.New("Node.js trusted release set is empty")
	}
	versions := make([]string, 0, len(releases))
	byVersion := make(map[string]NodeRelease, len(releases))
	installed := make([]string, 0, len(releases))
	for _, release := range releases {
		if err = release.Validate(); err != nil {
			return NodePlan{}, err
		}
		if previous, exists := byVersion[release.Version]; exists {
			if previous != release {
				return NodePlan{}, fmt.Errorf("Node.js trusted release %s has conflicting artifact identities", release.Version)
			}
			continue
		}
		byVersion[release.Version] = release
		versions = append(versions, release.Version)
		if _, lookupErr := p.Store.Lookup(release.GenerationID()); lookupErr == nil {
			installed = append(installed, release.Version)
		} else if !errors.Is(lookupErr, os.ErrNotExist) {
			return NodePlan{}, lookupErr
		}
	}
	resolution, err := ResolveVersion(selector, installed, versions, update, NodeVersionScheme{})
	if err != nil {
		return NodePlan{}, err
	}
	release, ok := byVersion[resolution.Version]
	if !ok {
		return NodePlan{}, errors.New("Node.js resolution escaped trusted release set")
	}
	return NodePlan{Resolution: resolution, Release: release, GenerationID: release.GenerationID()}, nil
}

func (p NodeProvider) Provision(ctx context.Context, plan NodePlan, source string) (Generation, error) {
	if err := plan.Release.Validate(); err != nil {
		return Generation{}, err
	}
	if plan.GenerationID != plan.Release.GenerationID() || plan.Resolution.Version != plan.Release.Version {
		return Generation{}, errors.New("Node.js install plan identity is inconsistent")
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return Generation{}, errors.New("Node.js artifact source must be a clean absolute path")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return Generation{}, err
	}
	if !info.Mode().IsRegular() {
		return Generation{}, errors.New("Node.js artifact source must be a regular file")
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
		return Generation{}, errors.New("Node.js artifact checksum mismatch")
	}
	generation, err := p.Store.Provision(ctx, plan.GenerationID, func(ctx context.Context, root string) error {
		artifact := Artifact{
			Name: "node", Version: plan.Release.Version, Filename: plan.Release.Filename(),
			URL: plan.Release.URL, SHA256: plan.Release.SHA256, Format: "tar.xz",
			InstallPath:     "/opt/loki/toolchain/node/" + plan.Release.Version,
			StripComponents: 1,
			Links: map[string]string{
				"node": "bin/node",
				"npm":  "bin/npm",
				"npx":  "bin/npx",
			},
		}
		if err := installArtifact(ctx, root, source, artifact); err != nil {
			return err
		}
		for _, name := range []string{"node", "npm", "npx"} {
			path := filepath.Join(root, "opt", "loki", "toolchain", "node", plan.Release.Version, "bin", name)
			executable, statErr := os.Stat(path)
			if statErr != nil || !executable.Mode().IsRegular() || executable.Mode().Perm()&0111 == 0 {
				return fmt.Errorf("Node.js artifact is missing executable %s", name)
			}
		}
		return nil
	})
	if err != nil {
		return Generation{}, err
	}
	return generation, nil
}

func (p NodePlan) Executables(generation Generation) (map[string]string, error) {
	if generation.ID != p.GenerationID || p.GenerationID != p.Release.GenerationID() {
		return nil, errors.New("Node.js generation does not match install plan")
	}
	base := filepath.Join(generation.Root, "opt", "loki", "toolchain", "node", p.Release.Version, "bin")
	result := map[string]string{
		"node": filepath.Join(base, "node"),
		"npm":  filepath.Join(base, "npm"),
		"npx":  filepath.Join(base, "npx"),
	}
	for name, path := range result {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return nil, fmt.Errorf("Node.js executable %s is unavailable", name)
		}
	}
	return result, nil
}
