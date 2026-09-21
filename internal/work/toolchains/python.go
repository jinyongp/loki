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

type PythonVersionScheme struct{}

func (PythonVersionScheme) ParseProjectSelector(raw string) (Selector, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "cpython@"))
	switch strings.ToLower(raw) {
	case "*", "python", "cpython", "latest":
		return NewSelector(SelectorFloating, "*")
	}
	parts, err := parseNumericVersionParts(raw, 1, 3)
	if err != nil {
		return Selector{}, fmt.Errorf("invalid Python selector %q", raw)
	}
	kind := SelectorPartial
	if len(parts) == 3 {
		kind = SelectorExact
	}
	return NewSelector(kind, joinVersionParts(parts))
}

func (PythonVersionScheme) NormalizeVersion(raw string) (string, error) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	parts, err := parseNumericVersionParts(raw, 3, 3)
	if err != nil {
		return "", fmt.Errorf("invalid Python version %q", raw)
	}
	return joinVersionParts(parts), nil
}

func (PythonVersionScheme) Match(selector Selector, version string) (bool, error) {
	version, err := (PythonVersionScheme{}).NormalizeVersion(version)
	if err != nil {
		return false, err
	}
	switch selector.Kind {
	case SelectorFloating:
		return true, nil
	case SelectorExact:
		exact, err := (PythonVersionScheme{}).NormalizeVersion(selector.Value)
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
		return false, errors.New("invalid Python selector kind")
	}
}

func (PythonVersionScheme) Compare(left, right string) (int, error) {
	leftParts, err := pythonVersionParts(left)
	if err != nil {
		return 0, err
	}
	rightParts, err := pythonVersionParts(right)
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

func pythonVersionParts(raw string) ([3]uint64, error) {
	var result [3]uint64
	normalized, err := (PythonVersionScheme{}).NormalizeVersion(raw)
	if err != nil {
		return result, err
	}
	parts, _ := parseNumericVersionParts(normalized, 3, 3)
	copy(result[:], parts)
	return result, nil
}

type pythonRequirementClause struct {
	operator string
	version  [3]uint64
	prefix   int
}

type PythonRequirement struct {
	Raw     string
	clauses []pythonRequirementClause
}

func ParsePythonRequirement(raw string) (PythonRequirement, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00;") {
		return PythonRequirement{}, errors.New("Python requires-python is empty or unsupported")
	}
	requirement := PythonRequirement{Raw: raw}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		operator := ""
		for _, candidate := range []string{"~=", "==", "!=", ">=", "<=", ">", "<"} {
			if strings.HasPrefix(item, candidate) {
				operator = candidate
				item = strings.TrimSpace(strings.TrimPrefix(item, candidate))
				break
			}
		}
		if operator == "" {
			return PythonRequirement{}, errors.New("Python requires-python contains an unsupported specifier")
		}
		wildcard := strings.HasSuffix(item, ".*")
		if wildcard {
			if operator != "==" && operator != "!=" {
				return PythonRequirement{}, errors.New("Python wildcard specifier requires == or !=")
			}
			item = strings.TrimSuffix(item, ".*")
		}
		parts, err := parseNumericVersionParts(item, 1, 3)
		if err != nil {
			return PythonRequirement{}, errors.New("Python requires-python contains an unsupported version")
		}
		clause := pythonRequirementClause{operator: operator, prefix: len(parts)}
		copy(clause.version[:], parts)
		if !wildcard && operator != "~=" && len(parts) != 3 {
			for index := len(parts); index < 3; index++ {
				clause.version[index] = 0
			}
			clause.prefix = 3
		}
		requirement.clauses = append(requirement.clauses, clause)
	}
	return requirement, nil
}

func (r PythonRequirement) Match(version string) (bool, error) {
	parts, err := pythonVersionParts(version)
	if err != nil {
		return false, err
	}
	for _, clause := range r.clauses {
		comparison := comparePythonParts(parts, clause.version)
		match := false
		switch clause.operator {
		case ">=":
			match = comparison >= 0
		case ">":
			match = comparison > 0
		case "<=":
			match = comparison <= 0
		case "<":
			match = comparison < 0
		case "==", "!=":
			match = true
			for index := 0; index < clause.prefix; index++ {
				if parts[index] != clause.version[index] {
					match = false
					break
				}
			}
			if clause.operator == "!=" {
				match = !match
			}
		case "~=":
			match = comparison >= 0
			if match {
				upperPrefix := clause.prefix - 1
				if upperPrefix < 1 {
					upperPrefix = 1
				}
				for index := 0; index < upperPrefix; index++ {
					if parts[index] != clause.version[index] {
						match = false
						break
					}
				}
			}
		default:
			return false, errors.New("Python requirement clause is invalid")
		}
		if !match {
			return false, nil
		}
	}
	return true, nil
}

func comparePythonParts(left, right [3]uint64) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

type PythonRelease struct {
	Version string `json:"version"`
	Build   string `json:"build"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

func (r PythonRelease) Filename() string {
	return "cpython-" + r.Version + "+" + r.Build + "-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz"
}

func (r PythonRelease) Validate() error {
	version, err := (PythonVersionScheme{}).NormalizeVersion(r.Version)
	if err != nil || version != r.Version {
		return errors.New("Python release version must be canonical")
	}
	if len(r.Build) != 8 {
		return errors.New("Python standalone build must use YYYYMMDD")
	}
	for _, ch := range r.Build {
		if ch < '0' || ch > '9' {
			return errors.New("Python standalone build must use YYYYMMDD")
		}
	}
	if !sha256Text.MatchString(r.SHA256) {
		return errors.New("Python release checksum must be a lowercase sha256 digest")
	}
	parsed, err := url.Parse(r.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Python release URL must use the canonical GitHub HTTPS origin")
	}
	expected := "/astral-sh/python-build-standalone/releases/download/" + r.Build + "/" + r.Filename()
	if parsed.Path != expected {
		return fmt.Errorf("Python release URL path must be %s", expected)
	}
	return nil
}

func (r PythonRelease) GenerationID() string {
	sum := sha256.Sum256([]byte("loki-python-generation:v1\n" + r.Version + "\n" + r.Build + "\n" + r.URL + "\n" + r.SHA256 + "\n"))
	return hex.EncodeToString(sum[:])
}

type PythonPlan struct {
	Resolution   VersionResolution
	Release      PythonRelease
	GenerationID string
}

type PythonProvider struct{ Store GenerationStore }

func (p PythonProvider) ResolveSelector(raw string, releases []PythonRelease, update bool) (PythonPlan, error) {
	selector, err := ParseProjectSelector(raw, PythonVersionScheme{})
	if err != nil {
		return PythonPlan{}, err
	}
	return p.resolve(selector, nil, releases, update)
}

func (p PythonProvider) ResolveProject(selectorRaw, requirementRaw string, releases []PythonRelease, update bool) (PythonPlan, error) {
	selector := Selector{Kind: SelectorFloating, Value: "*"}
	var requirement *PythonRequirement
	var err error
	if strings.TrimSpace(selectorRaw) != "" {
		selector, err = ParseProjectSelector(selectorRaw, PythonVersionScheme{})
		if err != nil {
			return PythonPlan{}, err
		}
	}
	if strings.TrimSpace(requirementRaw) != "" {
		parsed, parseErr := ParsePythonRequirement(requirementRaw)
		if parseErr != nil {
			return PythonPlan{}, parseErr
		}
		requirement = &parsed
	}
	return p.resolve(selector, requirement, releases, update)
}

func (p PythonProvider) ResolveRequirement(raw string, releases []PythonRelease, update bool) (PythonPlan, error) {
	requirement, err := ParsePythonRequirement(raw)
	if err != nil {
		return PythonPlan{}, err
	}
	return p.resolve(Selector{Kind: SelectorFloating, Value: "*"}, &requirement, releases, update)
}

func (p PythonProvider) resolve(selector Selector, requirement *PythonRequirement, releases []PythonRelease, update bool) (PythonPlan, error) {
	if len(releases) == 0 {
		return PythonPlan{}, errors.New("Python trusted release set is empty")
	}
	permitted := make([]string, 0, len(releases))
	installed := make([]string, 0, len(releases))
	byVersion := make(map[string]PythonRelease, len(releases))
	for _, release := range releases {
		if err := release.Validate(); err != nil {
			return PythonPlan{}, err
		}
		if requirement != nil {
			match, err := requirement.Match(release.Version)
			if err != nil {
				return PythonPlan{}, err
			}
			if !match {
				continue
			}
		}
		if previous, exists := byVersion[release.Version]; exists {
			if previous != release {
				return PythonPlan{}, fmt.Errorf("Python trusted release %s has conflicting artifact identities", release.Version)
			}
			continue
		}
		byVersion[release.Version] = release
		permitted = append(permitted, release.Version)
		if _, err := p.Store.Lookup(release.GenerationID()); err == nil {
			installed = append(installed, release.Version)
		} else if !errors.Is(err, os.ErrNotExist) {
			return PythonPlan{}, err
		}
	}
	resolution, err := ResolveVersion(selector, installed, permitted, update, PythonVersionScheme{})
	if err != nil {
		return PythonPlan{}, err
	}
	release, ok := byVersion[resolution.Version]
	if !ok {
		return PythonPlan{}, errors.New("Python resolution escaped trusted release set")
	}
	return PythonPlan{Resolution: resolution, Release: release, GenerationID: release.GenerationID()}, nil
}

func (p PythonProvider) Provision(ctx context.Context, plan PythonPlan, source string) (Generation, error) {
	if err := plan.Release.Validate(); err != nil {
		return Generation{}, err
	}
	if plan.GenerationID != plan.Release.GenerationID() || plan.Resolution.Version != plan.Release.Version {
		return Generation{}, errors.New("Python install plan identity is inconsistent")
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return Generation{}, errors.New("Python artifact source must be a clean absolute path")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return Generation{}, errors.New("Python artifact source must be a regular file")
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
		return Generation{}, errors.New("Python artifact checksum mismatch")
	}
	return p.Store.Provision(ctx, plan.GenerationID, func(ctx context.Context, root string) error {
		majorMinor := strings.Join(strings.Split(plan.Release.Version, ".")[:2], ".")
		artifact := Artifact{
			Name: "python", Version: plan.Release.Version, Filename: plan.Release.Filename(),
			URL: plan.Release.URL, SHA256: plan.Release.SHA256, Format: "tar.gz",
			InstallPath: "/opt/loki/toolchain/python/" + plan.Release.Version, StripComponents: 1,
			Links: map[string]string{
				"python":  "bin/python3",
				"python3": "bin/python3",
			},
		}
		if err := installArtifact(ctx, root, source, artifact); err != nil {
			return err
		}
		base := filepath.Join(root, "opt", "loki", "toolchain", "python", plan.Release.Version, "bin")
		for _, name := range []string{"python3", "python" + majorMinor} {
			info, err := os.Stat(filepath.Join(base, name))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return fmt.Errorf("Python artifact is missing executable %s", name)
			}
		}
		return nil
	})
}
