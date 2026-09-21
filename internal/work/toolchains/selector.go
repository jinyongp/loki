package toolchain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type SelectorKind string

const (
	SelectorExact    SelectorKind = "exact"
	SelectorPartial  SelectorKind = "partial"
	SelectorFloating SelectorKind = "floating"
)

type Selector struct {
	Kind  SelectorKind
	Value string
}

func NewSelector(kind SelectorKind, value string) (Selector, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "\r\n\x00") {
		return Selector{}, errors.New("toolchain selector contains invalid control characters")
	}
	switch kind {
	case SelectorExact, SelectorPartial:
		if value == "" {
			return Selector{}, errors.New("toolchain exact/partial selector is empty")
		}
	case SelectorFloating:
		if value == "" {
			value = "*"
		}
	default:
		return Selector{}, errors.New("toolchain selector kind is invalid")
	}
	return Selector{Kind: kind, Value: value}, nil
}

type VersionScheme interface {
	ParseProjectSelector(string) (Selector, error)
	NormalizeVersion(string) (string, error)
	Match(Selector, string) (bool, error)
	Compare(left, right string) (int, error)
}

type VersionResolution struct {
	Selector  Selector
	Version   string
	Installed bool
	Acquire   bool
}

func ParseProjectSelector(raw string, scheme VersionScheme) (Selector, error) {
	if scheme == nil {
		return Selector{}, errors.New("toolchain version scheme is required")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00") {
		return Selector{}, errors.New("toolchain project selector is empty or contains control characters")
	}
	selector, err := scheme.ParseProjectSelector(raw)
	if err != nil {
		return Selector{}, err
	}
	normalized, err := NewSelector(selector.Kind, selector.Value)
	if err != nil {
		return Selector{}, err
	}
	return normalized, nil
}

func ResolveVersion(
	selector Selector,
	installed []string,
	permitted []string,
	update bool,
	scheme VersionScheme,
) (VersionResolution, error) {
	if scheme == nil {
		return VersionResolution{}, errors.New("toolchain version scheme is required")
	}
	selector, err := NewSelector(selector.Kind, selector.Value)
	if err != nil {
		return VersionResolution{}, err
	}
	allowed, err := normalizeVersions(permitted, scheme)
	if err != nil {
		return VersionResolution{}, fmt.Errorf("normalize permitted toolchain versions: %w", err)
	}
	if len(allowed) == 0 {
		return VersionResolution{}, errors.New("toolchain has no administrator-permitted versions")
	}
	present, err := normalizeVersions(installed, scheme)
	if err != nil {
		return VersionResolution{}, fmt.Errorf("normalize installed toolchain versions: %w", err)
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, version := range allowed {
		allowedSet[version] = true
	}

	matchingAllowed, err := matchingVersions(selector, allowed, scheme)
	if err != nil {
		return VersionResolution{}, err
	}
	if len(matchingAllowed) == 0 {
		return VersionResolution{}, errors.New("toolchain selector has no administrator-permitted match")
	}
	matchingInstalled := make([]string, 0, len(present))
	for _, version := range present {
		if !allowedSet[version] {
			continue
		}
		match, matchErr := scheme.Match(selector, version)
		if matchErr != nil {
			return VersionResolution{}, matchErr
		}
		if match {
			matchingInstalled = append(matchingInstalled, version)
		}
	}

	bestAllowed, err := highestVersion(matchingAllowed, scheme)
	if err != nil {
		return VersionResolution{}, err
	}
	if selector.Kind == SelectorExact && len(matchingAllowed) != 1 {
		return VersionResolution{}, errors.New("exact toolchain selector resolved ambiguously")
	}
	if !update && len(matchingInstalled) > 0 {
		bestInstalled, bestErr := highestVersion(matchingInstalled, scheme)
		if bestErr != nil {
			return VersionResolution{}, bestErr
		}
		return VersionResolution{
			Selector: selector, Version: bestInstalled, Installed: true, Acquire: false,
		}, nil
	}
	if slices.Contains(matchingInstalled, bestAllowed) {
		return VersionResolution{
			Selector: selector, Version: bestAllowed, Installed: true, Acquire: false,
		}, nil
	}
	return VersionResolution{
		Selector: selector, Version: bestAllowed, Installed: false, Acquire: true,
	}, nil
}

func normalizeVersions(versions []string, scheme VersionScheme) ([]string, error) {
	result := make([]string, 0, len(versions))
	seen := map[string]bool{}
	for _, version := range versions {
		normalized, err := scheme.NormalizeVersion(version)
		if err != nil {
			return nil, err
		}
		if normalized == "" || strings.ContainsAny(normalized, "\r\n\x00") {
			return nil, errors.New("toolchain version normalizer returned an invalid version")
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		result = append(result, normalized)
	}
	return result, nil
}

func matchingVersions(selector Selector, versions []string, scheme VersionScheme) ([]string, error) {
	result := make([]string, 0, len(versions))
	for _, version := range versions {
		match, err := scheme.Match(selector, version)
		if err != nil {
			return nil, err
		}
		if match {
			result = append(result, version)
		}
	}
	return result, nil
}

func highestVersion(versions []string, scheme VersionScheme) (string, error) {
	if len(versions) == 0 {
		return "", errors.New("toolchain version set is empty")
	}
	best := versions[0]
	for _, version := range versions[1:] {
		comparison, err := scheme.Compare(version, best)
		if err != nil {
			return "", err
		}
		if comparison > 0 {
			best = version
		}
	}
	return best, nil
}
