package toolchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func pythonRelease(version, build, checksum string) PythonRelease {
	filename := "cpython-" + version + "+" + build + "-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz"
	return PythonRelease{
		Version: version,
		Build:   build,
		URL:     "https://github.com/astral-sh/python-build-standalone/releases/download/" + build + "/" + filename,
		SHA256:  checksum,
	}
}

func writePythonArchive(t *testing.T, version, build string) (string, string) {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "python", "bin")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	minor := strings.Join(strings.Split(version, ".")[:2], ".")
	for _, name := range []string{"python3", "python" + minor} {
		if err := os.WriteFile(filepath.Join(base, name), []byte("#!/bin/sh\nprintf 'python "+version+"\n'\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	filename := "cpython-" + version + "+" + build + "-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz"
	archive := filepath.Join(t.TempDir(), filename)
	command := exec.Command("tar", "-czf", archive, "-C", root, "python")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create Python archive: %v: %s", err, output)
	}
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	return archive, fmt.Sprintf("%x", sha256.Sum256(payload))
}

func TestPythonVersionSchemeAndRequirements(t *testing.T) {
	scheme := PythonVersionScheme{}
	for raw, want := range map[string]Selector{
		"3.14.7": {Kind: SelectorExact, Value: "3.14.7"},
		"3.14":   {Kind: SelectorPartial, Value: "3.14"},
		"3":      {Kind: SelectorPartial, Value: "3"},
		"python": {Kind: SelectorFloating, Value: "*"},
	} {
		got, err := ParseProjectSelector(raw, scheme)
		if err != nil || got != want {
			t.Fatalf("Python selector %q = %#v, %v; want %#v", raw, got, err, want)
		}
	}
	for _, raw := range []string{">=3.12,<3.15", "~=3.14", "==3.14.*", ">=3.13,!=3.14.0"} {
		requirement, err := ParsePythonRequirement(raw)
		if err != nil {
			t.Fatalf("ParsePythonRequirement(%q): %v", raw, err)
		}
		_ = requirement
	}

	requirement, _ := ParsePythonRequirement(">=3.13,<3.15,!=3.14.0")
	for version, want := range map[string]bool{
		"3.12.9":  false,
		"3.13.15": true,
		"3.14.0":  false,
		"3.14.7":  true,
		"3.15.0":  false,
	} {
		got, err := requirement.Match(version)
		if err != nil || got != want {
			t.Fatalf("%q matches %q = %v, %v; want %v", version, requirement.Raw, got, err, want)
		}
	}

	compatible, _ := ParsePythonRequirement("~=3.14.2")
	for version, want := range map[string]bool{"3.14.2": true, "3.14.7": true, "3.15.0": false} {
		got, err := compatible.Match(version)
		if err != nil || got != want {
			t.Fatalf("%q ~=3.14.2 = %v, %v; want %v", version, got, err, want)
		}
	}
}

func TestPythonProviderResolutionHonorsRequirementAndInstalledVersions(t *testing.T) {
	store := generationStoreFixture(t)
	old := pythonRelease("3.13.15", "20260805", strings.Repeat("a", 64))
	current := pythonRelease("3.14.7", "20260901", strings.Repeat("b", 64))
	if _, err := store.Provision(t.Context(), old.GenerationID(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	provider := PythonProvider{Store: store}

	ordinary, err := provider.ResolveRequirement(">=3.13,<3.15", []PythonRelease{old, current}, false)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Resolution.Version != old.Version || !ordinary.Resolution.Installed || ordinary.Resolution.Acquire {
		t.Fatalf("ordinary Python resolution = %#v", ordinary)
	}
	update, err := provider.ResolveRequirement(">=3.13,<3.15", []PythonRelease{old, current}, true)
	if err != nil {
		t.Fatal(err)
	}
	if update.Resolution.Version != current.Version || update.Resolution.Installed || !update.Resolution.Acquire {
		t.Fatalf("Python update resolution = %#v", update)
	}
	if _, err = provider.ResolveRequirement(">=3.15", []PythonRelease{old, current}, false); err == nil {
		t.Fatal("unsatisfied Python requirement was accepted")
	}
}

func TestPythonProviderProvisionStandaloneBuild(t *testing.T) {
	const version = "3.14.7"
	const build = "20260901"
	source, checksum := writePythonArchive(t, version, build)
	release := pythonRelease(version, build, checksum)
	provider := PythonProvider{Store: generationStoreFixture(t)}
	plan, err := provider.ResolveSelector("3.14", []PythonRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := provider.Provision(t.Context(), plan, source)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(generation.Root, "opt", "loki", "toolchain", "python", version, "bin")
	for _, name := range []string{"python3", "python3.14"} {
		info, err := os.Stat(filepath.Join(base, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0555 {
			t.Fatalf("%s = %v, %v", name, info, err)
		}
	}
}

func TestPythonReleaseRejectsUntrustedIdentityAndTamperedArtifact(t *testing.T) {
	valid := pythonRelease("3.14.7", "20260901", strings.Repeat("c", 64))
	for _, release := range []PythonRelease{
		{Version: "v3.14.7", Build: valid.Build, URL: valid.URL, SHA256: valid.SHA256},
		{Version: valid.Version, Build: "2026-08-05", URL: valid.URL, SHA256: valid.SHA256},
		{Version: valid.Version, Build: valid.Build, URL: strings.Replace(valid.URL, "github.com", "example.test", 1), SHA256: valid.SHA256},
	} {
		if err := release.Validate(); err == nil {
			t.Fatalf("invalid Python release accepted: %#v", release)
		}
	}

	source, checksum := writePythonArchive(t, "3.14.7", "20260901")
	release := pythonRelease("3.14.7", "20260901", checksum)
	provider := PythonProvider{Store: generationStoreFixture(t)}
	plan, err := provider.ResolveSelector("3.14.7", []PythonRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Provision(t.Context(), plan, source); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered Python artifact error = %v", err)
	}
}
