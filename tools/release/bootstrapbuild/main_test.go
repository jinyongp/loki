package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"loki/internal/host/releases"
)

type fakeBuildRunner struct {
	cwd  string
	args []string
	env  []string
	err  error
}

func (r *fakeBuildRunner) Run(_ context.Context, cwd string, args, environment []string) error {
	r.cwd = cwd
	r.args = append([]string(nil), args...)
	r.env = append([]string(nil), environment...)
	if r.err != nil {
		return r.err
	}
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-o" {
			return os.WriteFile(args[index+1], []byte("bootstrap-binary"), 0600)
		}
	}
	return errors.New("builder output path was not supplied")
}

func bootstrapBuildManifest(t *testing.T, version string) []byte {
	t.Helper()
	binary := []byte("host-binary")
	sum := sha256.Sum256(binary)
	generation, err := releases.NewGeneration(releases.GenerationSpec{
		Version:          version,
		ReleasedAt:       time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
		HostBinaryDigest: "sha256:" + hex.EncodeToString(sum[:]),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		ConfigSchema:     1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: releases.Compatibility{
			Config: releases.SchemaRange{Min: 1, Max: 1}, Policy: releases.SchemaRange{Min: 1, Max: 1},
			Toolchain: releases.SchemaRange{Min: 1, Max: 1}, State: releases.SchemaRange{Min: 1, Max: 1},
		},
		Rollback: releases.RollbackCoverage{StateSnapshot: true, ConfigSnapshot: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := func(path, fill string) releases.TargetDescriptor {
		return releases.TargetDescriptor{Path: path, Length: 1, SHA256: strings.Repeat(fill, 64)}
	}
	raw, err := releases.EncodeReleaseManifest(releases.ReleaseManifest{
		Version:          releases.ReleaseManifestVersion,
		Generation:       generation,
		HostBinary:       releases.TargetDescriptor{Path: "releases/bin/loki-" + version, Length: int64(len(binary)), SHA256: hex.EncodeToString(sum[:])},
		HostAssets:       target("releases/assets/loki-host-"+version+".tar.gz", "d"),
		ToolchainCatalog: target("toolchains/catalogs/"+version+".json", "e"),
		Provenance:       target("releases/provenance/"+version+".bundle.json", "f"),
		Notices:          target("releases/notices/"+version+".tar.gz", "1"),
		ReleaseNotes:     target("releases/notes/"+version+".md", "2"),
		SupportedHosts:   []releases.SupportedHost{{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"}},
		Runtime:          releases.RuntimeRequirements{DockerMin: "29.8.1", ComposeMin: "5.5.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func bootstrapBuildFixture(t *testing.T) (buildOptions, []byte) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manifest := bootstrapBuildManifest(t, "1.2.3")
	manifestPath := filepath.Join(t.TempDir(), "release-manifest.json")
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	return buildOptions{
		Output:          filepath.Join(t.TempDir(), "loki-bootstrap"),
		ReleaseTag:      "v1.2.3",
		ReleaseManifest: manifestPath,
		SourceRoot:      source,
		GOOS:            "linux",
		GOARCH:          "amd64",
	}, manifest
}

func TestBuildBootstrapEmbedsReleaseBindingAndPublishesAtomically(t *testing.T) {
	options, manifest := bootstrapBuildFixture(t)
	runner := &fakeBuildRunner{}
	environment := []string{
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8",
		"CGO_ENABLED=1", "GOOS=windows", "GOARCH=arm64",
		"GOFLAGS=-race", "GOEXPERIMENT=arenas", "GOAMD64=v4",
		"GOTOOLCHAIN=auto", "GOENV=/tmp/goenv", "GOWORK=/tmp/go.work",
	}
	if err := buildBootstrap(t.Context(), options, runner, environment); err != nil {
		t.Fatal(err)
	}
	if runner.cwd != options.SourceRoot {
		t.Fatalf("builder cwd = %q", runner.cwd)
	}
	if !slices.Contains(runner.args, "-trimpath") || !slices.Contains(runner.args, "-buildvcs=false") ||
		!slices.Contains(runner.args, "./cmd/bootstrap") {
		t.Fatalf("builder args = %#v", runner.args)
	}
	joined := strings.Join(runner.args, "\n")
	if !strings.Contains(joined, "main.releaseTag="+options.ReleaseTag) ||
		!strings.Contains(joined, "main.releaseManifestBase64="+base64.StdEncoding.EncodeToString(manifest)) {
		t.Fatalf("embedded linker flags = %#v", runner.args)
	}
	for _, want := range []string{
		"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOAMD64=v1",
		"GOFLAGS=", "GOEXPERIMENT=", "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off",
	} {
		if !slices.Contains(runner.env, want) {
			t.Fatalf("builder environment missing %q: %#v", want, runner.env)
		}
	}
	info, err := os.Stat(options.Output)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0755 {
		t.Fatalf("bootstrap output = %v, %v", info, err)
	}
}

func TestBuildBootstrapFailsClosedBeforeRunner(t *testing.T) {
	options, _ := bootstrapBuildFixture(t)
	tests := map[string]func(*buildOptions){
		"relative-output":   func(value *buildOptions) { value.Output = "bootstrap" },
		"relative-manifest": func(value *buildOptions) { value.ReleaseManifest = "release-manifest.json" },
		"wrong-tag":         func(value *buildOptions) { value.ReleaseTag = "v9.9.9" },
		"bad-goos":          func(value *buildOptions) { value.GOOS = "linux;sh" },
		"bad-goarch":        func(value *buildOptions) { value.GOARCH = "../amd64" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := options
			changed.Output = filepath.Join(t.TempDir(), "bootstrap")
			mutate(&changed)
			runner := &fakeBuildRunner{}
			if err := buildBootstrap(t.Context(), changed, runner, nil); err == nil {
				t.Fatal("invalid bootstrap build options were accepted")
			}
			if len(runner.args) != 0 {
				t.Fatalf("runner was called: %#v", runner.args)
			}
		})
	}
}

func TestBuildBootstrapRejectsExistingOutput(t *testing.T) {
	options, _ := bootstrapBuildFixture(t)
	if err := os.WriteFile(options.Output, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeBuildRunner{}
	if err := buildBootstrap(t.Context(), options, runner, nil); err == nil {
		t.Fatal("existing output was overwritten")
	}
	if len(runner.args) != 0 {
		t.Fatalf("runner called for existing output: %#v", runner.args)
	}
}
