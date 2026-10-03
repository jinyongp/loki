package toolchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func rustArtifactFilename(channel, version, component, target string) string {
	token := version
	if channel == "beta" || channel == "nightly" {
		token = channel
	}
	name := component + "-" + token
	if target != "" {
		name += "-" + target
	}
	return name + ".tar.xz"
}

func rustMetadataRelease(channel, version, date string, components []string, targets []string) RustRelease {
	artifacts := make([]RustArtifact, 0, len(components)+len(targets))
	for _, component := range components {
		target := rustDefaultHost
		if component == "rust-src" {
			target = ""
		}
		filename := rustArtifactFilename(channel, version, component, target)
		artifacts = append(artifacts, RustArtifact{
			Component:       component,
			Target:          target,
			Filename:        filename,
			URL:             "https://static.rust-lang.org/dist/" + date + "/" + filename,
			SHA256:          fmt.Sprintf("%064x", len(artifacts)+1),
			StripComponents: 2,
		})
	}
	for _, target := range targets {
		filename := rustArtifactFilename(channel, version, "rust-std", target)
		artifacts = append(artifacts, RustArtifact{
			Component:       "rust-std",
			Target:          target,
			Filename:        filename,
			URL:             "https://static.rust-lang.org/dist/" + date + "/" + filename,
			SHA256:          fmt.Sprintf("%064x", len(artifacts)+1),
			StripComponents: 2,
		})
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].key() < artifacts[j].key() })
	return RustRelease{
		Channel:   channel,
		Version:   version,
		Date:      date,
		Host:      rustDefaultHost,
		Artifacts: artifacts,
	}
}

func writeRustArtifactArchive(t *testing.T, artifact RustArtifact) (string, string) {
	t.Helper()
	root := t.TempDir()
	packageRoot := filepath.Join(root, strings.TrimSuffix(artifact.Filename, ".tar.xz"), "payload")
	write := func(relative, body string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(packageRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	switch artifact.Component {
	case "cargo":
		write("bin/cargo", "#!/bin/sh\nprintf 'cargo fixture\\n'\n", 0755)
	case "rustc":
		write("bin/rustc", "#!/bin/sh\nprintf 'rustc fixture\\n'\n", 0755)
	case "rustfmt":
		write("bin/rustfmt", "#!/bin/sh\nprintf 'rustfmt fixture\\n'\n", 0755)
		write("bin/cargo-fmt", "#!/bin/sh\nprintf 'cargo-fmt fixture\\n'\n", 0755)
	case "clippy":
		write("bin/clippy-driver", "#!/bin/sh\nprintf 'clippy fixture\\n'\n", 0755)
		write("bin/cargo-clippy", "#!/bin/sh\nprintf 'cargo-clippy fixture\\n'\n", 0755)
	case "rust-analyzer":
		write("bin/rust-analyzer", "#!/bin/sh\nprintf 'rust-analyzer fixture\\n'\n", 0755)
	case "rust-docs":
		write("share/doc/rust/html/index.html", "docs\n", 0644)
	case "rust-src":
		write("lib/rustlib/src/rust/library/lib.rs", "// fixture\n", 0644)
	case "rust-std":
		write("lib/rustlib/"+artifact.Target+"/lib/libstd.rlib", "fixture\n", 0644)
	default:
		t.Fatalf("unsupported Rust fixture component %q", artifact.Component)
	}
	archive := filepath.Join(t.TempDir(), artifact.Filename)
	command := exec.Command("tar", "-cJf", archive, "-C", root, filepath.Base(filepath.Dir(packageRoot)))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create Rust archive: %v: %s", err, output)
	}
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	return archive, fmt.Sprintf("%x", sha256.Sum256(payload))
}

func writeRustReleaseBundle(t *testing.T, version, date string) (string, RustRelease) {
	t.Helper()
	release := rustMetadataRelease(
		"stable",
		version,
		date,
		[]string{"cargo", "clippy", "rust-analyzer", "rust-docs", "rust-src", "rustc", "rustfmt"},
		[]string{rustDefaultHost, "wasm32-unknown-unknown"},
	)
	artifactDirectory := filepath.Join(t.TempDir(), "artifacts")
	if err := os.Mkdir(artifactDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	for index := range release.Artifacts {
		source, checksum := writeRustArtifactArchive(t, release.Artifacts[index])
		release.Artifacts[index].SHA256 = checksum
		target := filepath.Join(artifactDirectory, release.Artifacts[index].Filename)
		if err := os.Rename(source, target); err != nil {
			t.Fatal(err)
		}
	}
	return artifactDirectory, release
}

func TestRustChannelSelectorsPreserveRustupSemantics(t *testing.T) {
	for raw, want := range map[string]RustChannelSelector{
		"stable":             {Channel: "stable"},
		"beta":               {Channel: "beta"},
		"nightly-2026-09-05": {Channel: "nightly", Date: "2026-09-05"},
		"1.98":               {VersionPrefix: "1.98"},
		"1.98.1":             {VersionPrefix: "1.98.1"},
		"1.99.0-beta.2":      {VersionPrefix: "1.99.0-beta.2"},
	} {
		got, err := ParseRustChannel(raw)
		if err != nil || got != want {
			t.Fatalf("Rust selector %q = %#v, %v; want %#v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "stable-x86_64-unknown-linux-gnu", "nightly-2026-99-99", "01.98", "1.98.01", ">=1.98"} {
		if _, err := ParseRustChannel(raw); err == nil {
			t.Fatalf("invalid Rust selector %q accepted", raw)
		}
	}

	stable := rustMetadataRelease("stable", "1.99.0", "2026-10-15",
		[]string{"cargo", "rustc"}, []string{rustDefaultHost})
	beta := rustMetadataRelease("beta", "1.99.0-beta.2", "2026-09-20",
		[]string{"cargo", "rustc"}, []string{rustDefaultHost})
	if !rustReleaseMatches(RustChannelSelector{VersionPrefix: "1.99"}, stable) ||
		rustReleaseMatches(RustChannelSelector{VersionPrefix: "1.99"}, beta) {
		t.Fatal("partial Rust version selector did not remain on the stable channel")
	}
}

func TestRustProviderReusesReleaseGenerationAcrossComponentProfiles(t *testing.T) {
	artifactDirectory, release := writeRustReleaseBundle(t, "1.98.1", "2026-09-03")
	store := generationStoreFixture(t)
	provider := RustProvider{Store: store}
	request := RustProjectRequest{
		Channel:    "1.98",
		Profile:    "default",
		Components: []string{"rust-analyzer", "rust-src"},
		Targets:    []string{"wasm32-unknown-unknown"},
	}
	plan, err := provider.Resolve(request, []RustRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Resolution.Acquire || plan.Resolution.Installed || plan.GenerationID != release.GenerationID() {
		t.Fatalf("initial Rust plan = %#v", plan)
	}
	generation, err := provider.Provision(t.Context(), plan, artifactDirectory)
	if err != nil {
		t.Fatal(err)
	}
	executables, err := plan.Executables(generation)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rustc", "cargo", "rustfmt", "cargo-fmt", "clippy-driver", "cargo-clippy", "rust-analyzer"} {
		info, statErr := os.Stat(executables[name])
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			t.Fatalf("Rust executable %s = %q, %v, %v", name, executables[name], info, statErr)
		}
	}
	for _, path := range []string{
		filepath.Join(generation.Root, "opt", "loki", "toolchain", "rust", release.Version, "active", "lib", "rustlib", rustDefaultHost, "lib"),
		filepath.Join(generation.Root, "opt", "loki", "toolchain", "rust", release.Version, "active", "lib", "rustlib", "wasm32-unknown-unknown", "lib"),
		filepath.Join(generation.Root, "opt", "loki", "toolchain", "rust", release.Version, "active", "lib", "rustlib", "src", "rust"),
	} {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			t.Fatalf("Rust component path %s = %v, %v", path, info, statErr)
		}
	}

	minimal, err := provider.Resolve(RustProjectRequest{Channel: "stable", Profile: "minimal"}, []RustRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	if minimal.GenerationID != plan.GenerationID || !minimal.Resolution.Installed || minimal.Resolution.Acquire {
		t.Fatalf("minimal Rust plan did not reuse release generation: %#v", minimal)
	}
	complete, err := provider.Resolve(RustProjectRequest{Channel: release.Version, Profile: "complete"}, []RustRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	if complete.GenerationID != plan.GenerationID || len(complete.Artifacts) <= 3 {
		t.Fatalf("complete Rust profile = %#v", complete)
	}
}

func TestRustProviderKeepsInstalledPermittedReleaseUntilExplicitUpdate(t *testing.T) {
	store := generationStoreFixture(t)
	provider := RustProvider{Store: store}
	old := rustMetadataRelease("stable", "1.97.1", "2026-08-06",
		[]string{"cargo", "rustc"}, []string{rustDefaultHost})
	current := rustMetadataRelease("stable", "1.98.1", "2026-09-03",
		[]string{"cargo", "rustc"}, []string{rustDefaultHost})
	if _, err := store.Provision(t.Context(), old.GenerationID(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	releases := []RustRelease{old, current}
	ordinary, err := provider.Resolve(RustProjectRequest{Channel: "stable", Profile: "minimal"}, releases, false)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Release.Version != old.Version || !ordinary.Resolution.Installed || ordinary.Resolution.Acquire {
		t.Fatalf("ordinary Rust resolution = %#v", ordinary)
	}
	update, err := provider.Resolve(RustProjectRequest{Channel: "stable", Profile: "minimal"}, releases, true)
	if err != nil {
		t.Fatal(err)
	}
	if update.Release.Version != current.Version || update.Resolution.Installed || !update.Resolution.Acquire {
		t.Fatalf("updated Rust resolution = %#v", update)
	}
}

func TestRustReleaseRejectsUntrustedOrIncompleteArtifacts(t *testing.T) {
	release := rustMetadataRelease("stable", "1.98.1", "2026-09-03",
		[]string{"cargo", "rustc"}, []string{rustDefaultHost})
	badOrigin := release
	badOrigin.Artifacts = append([]RustArtifact(nil), release.Artifacts...)
	badOrigin.Artifacts[0].URL = strings.Replace(badOrigin.Artifacts[0].URL, "static.rust-lang.org", "example.test", 1)
	if err := badOrigin.Validate(); err == nil {
		t.Fatal("Rust artifact outside the official dist origin was accepted")
	}
	missingStd := release
	missingStd.Artifacts = append([]RustArtifact(nil), release.Artifacts[:2]...)
	if err := missingStd.Validate(); err == nil || !strings.Contains(err.Error(), "required component") {
		t.Fatalf("incomplete Rust release error = %v", err)
	}

	artifactDirectory, provisioned := writeRustReleaseBundle(t, "1.98.1", "2026-09-03")
	provider := RustProvider{Store: generationStoreFixture(t)}
	plan, err := provider.Resolve(RustProjectRequest{Channel: provisioned.Version, Profile: "minimal"}, []RustRelease{provisioned}, false)
	if err != nil {
		t.Fatal(err)
	}
	tampered := filepath.Join(artifactDirectory, provisioned.Artifacts[0].Filename)
	if err = os.WriteFile(tampered, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Provision(t.Context(), plan, artifactDirectory); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered Rust artifact error = %v", err)
	}
}
