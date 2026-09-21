package toolchain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

type testVersionScheme struct{}

func (testVersionScheme) ParseProjectSelector(raw string) (Selector, error) {
	switch {
	case raw == "*", raw == "latest":
		return NewSelector(SelectorFloating, "*")
	case strings.Count(raw, ".") == 2:
		return NewSelector(SelectorExact, raw)
	case strings.Count(raw, ".") <= 1:
		return NewSelector(SelectorPartial, raw)
	default:
		return Selector{}, errors.New("invalid test selector")
	}
}

func (testVersionScheme) NormalizeVersion(raw string) (string, error) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid test version %q", raw)
	}
	for _, part := range parts {
		if _, err := strconv.Atoi(part); err != nil {
			return "", fmt.Errorf("invalid test version %q", raw)
		}
	}
	return strings.Join(parts, "."), nil
}

func (testVersionScheme) Match(selector Selector, version string) (bool, error) {
	version, err := (testVersionScheme{}).NormalizeVersion(version)
	if err != nil {
		return false, err
	}
	switch selector.Kind {
	case SelectorExact:
		exact, err := (testVersionScheme{}).NormalizeVersion(selector.Value)
		return version == exact, err
	case SelectorPartial:
		parts := strings.Split(selector.Value, ".")
		versionParts := strings.Split(version, ".")
		if len(parts) < 1 || len(parts) > 2 {
			return false, errors.New("invalid partial selector")
		}
		for index, part := range parts {
			if part != versionParts[index] {
				return false, nil
			}
		}
		return true, nil
	case SelectorFloating:
		return true, nil
	default:
		return false, errors.New("invalid selector kind")
	}
}

func (testVersionScheme) Compare(left, right string) (int, error) {
	leftParts, err := testVersionParts(left)
	if err != nil {
		return 0, err
	}
	rightParts, err := testVersionParts(right)
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

func testVersionParts(raw string) ([3]int, error) {
	var result [3]int
	normalized, err := (testVersionScheme{}).NormalizeVersion(raw)
	if err != nil {
		return result, err
	}
	for index, part := range strings.Split(normalized, ".") {
		result[index], _ = strconv.Atoi(part)
	}
	return result, nil
}

func TestParseProjectSelectorDelegatesVersionGrammar(t *testing.T) {
	scheme := testVersionScheme{}
	for raw, want := range map[string]Selector{
		"26.9.0": {Kind: SelectorExact, Value: "26.9.0"},
		"22.23":  {Kind: SelectorPartial, Value: "22.23"},
		"22":     {Kind: SelectorPartial, Value: "22"},
		"latest": {Kind: SelectorFloating, Value: "*"},
	} {
		got, err := ParseProjectSelector(raw, scheme)
		if err != nil || got != want {
			t.Fatalf("ParseProjectSelector(%q) = %#v, %v; want %#v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "22\n23", "22.23.1.4"} {
		if _, err := ParseProjectSelector(raw, scheme); err == nil {
			t.Fatalf("invalid selector %q accepted", raw)
		}
	}
}

func TestResolveVersionPrefersHighestInstalledPermittedMatch(t *testing.T) {
	scheme := testVersionScheme{}
	selector, err := ParseProjectSelector("26", scheme)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := ResolveVersion(
		selector,
		[]string{"26.1.0", "25.9.0"},
		[]string{"26.1.0", "26.2.0", "27.0.0"},
		false,
		scheme,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Version != "26.1.0" || !resolution.Installed || resolution.Acquire {
		t.Fatalf("ordinary partial resolution = %#v", resolution)
	}

	resolution, err = ResolveVersion(
		selector,
		[]string{"26.1.0", "26.2.0"},
		[]string{"26.1.0", "26.2.0", "27.0.0"},
		false,
		scheme,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Version != "26.2.0" || !resolution.Installed || resolution.Acquire {
		t.Fatalf("shared-store floating effect = %#v", resolution)
	}
}

func TestResolveVersionAcquiresOnlyMissingOrExplicitUpdateMatch(t *testing.T) {
	scheme := testVersionScheme{}
	selector, _ := ParseProjectSelector("22.23", scheme)

	missing, err := ResolveVersion(
		selector,
		nil,
		[]string{"22.22.9", "22.23.0", "22.23.4", "23.0.0"},
		false,
		scheme,
	)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Version != "22.23.4" || missing.Installed || !missing.Acquire {
		t.Fatalf("missing match resolution = %#v", missing)
	}

	ordinary, err := ResolveVersion(
		selector,
		[]string{"22.23.0"},
		[]string{"22.23.0", "22.23.4"},
		false,
		scheme,
	)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Version != "22.23.0" || !ordinary.Installed || ordinary.Acquire {
		t.Fatalf("ordinary installed resolution = %#v", ordinary)
	}

	update, err := ResolveVersion(
		selector,
		[]string{"22.23.0"},
		[]string{"22.23.0", "22.23.4"},
		true,
		scheme,
	)
	if err != nil {
		t.Fatal(err)
	}
	if update.Version != "22.23.4" || update.Installed || !update.Acquire {
		t.Fatalf("explicit update resolution = %#v", update)
	}
}

func TestResolveVersionEnforcesAdministratorPermittedSet(t *testing.T) {
	scheme := testVersionScheme{}
	exact, _ := ParseProjectSelector("26.9.0", scheme)
	if _, err := ResolveVersion(exact, []string{"26.9.0"}, []string{"26.8.0"}, false, scheme); err == nil {
		t.Fatal("exact selector escaped administrator policy")
	}

	floating, _ := ParseProjectSelector("*", scheme)
	resolution, err := ResolveVersion(
		floating,
		[]string{"99.0.0"},
		[]string{"26.8.0", "26.9.0"},
		false,
		scheme,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Version != "26.9.0" || resolution.Installed || !resolution.Acquire {
		t.Fatalf("unpermitted installed version influenced resolution: %#v", resolution)
	}
}

func TestResolveVersionExactAndNormalization(t *testing.T) {
	scheme := testVersionScheme{}
	exact, _ := ParseProjectSelector("26.9.0", scheme)
	resolution, err := ResolveVersion(
		exact,
		[]string{"v26.9.0"},
		[]string{"26.9.0", "27.0.0"},
		true,
		scheme,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Version != "26.9.0" || !resolution.Installed || resolution.Acquire {
		t.Fatalf("exact normalized resolution = %#v", resolution)
	}
}
