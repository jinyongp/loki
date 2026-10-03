package toolchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func goRelease(version, digest string) GoRelease {
	return GoRelease{
		Version: version,
		URL:     "https://go.dev/dl/go" + version + ".linux-amd64.tar.gz",
		SHA256:  digest,
	}
}

func TestGoVersionSchemeUsesGoReleaseOrdering(t *testing.T) {
	scheme := GoVersionScheme{}
	partial, err := scheme.ParseProjectSelector("go1.27")
	if err != nil {
		t.Fatal(err)
	}
	if partial.Kind != SelectorPartial || partial.Value != "1.27" {
		t.Fatalf("partial selector = %#v", partial)
	}
	if match, err := scheme.Match(partial, "1.27.1"); err != nil || !match {
		t.Fatalf("partial selector match = %v, %v", match, err)
	}
	exact, err := scheme.ParseProjectSelector("go1.28rc1")
	if err != nil {
		t.Fatal(err)
	}
	if exact.Kind != SelectorExact || exact.Value != "1.28rc1" {
		t.Fatalf("prerelease selector = %#v", exact)
	}
	if comparison, err := scheme.Compare("1.28rc1", "1.27.9"); err != nil || comparison <= 0 {
		t.Fatalf("Go version comparison = %d, %v", comparison, err)
	}
	if _, err := scheme.NormalizeVersion("1.27"); err == nil {
		t.Fatal("post-1.21 release without patch version was accepted")
	}
}

func TestGoProviderRespectsProjectMinimumAndPreferredToolchain(t *testing.T) {
	store := generationStoreFixture(t)
	old := goRelease("1.26.3", strings.Repeat("a", 64))
	current := goRelease("1.27.1", strings.Repeat("b", 64))
	if _, err := store.Provision(t.Context(), old.GenerationID(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	provider := GoProvider{Store: store}
	request := GoProjectRequest{Minimum: "1.26", Toolchain: "go1.26.3"}

	plan, err := provider.Resolve(request, []GoRelease{old, current}, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Release.Version != old.Version || !plan.Resolution.Installed || plan.Resolution.Acquire {
		t.Fatalf("ordinary Go resolution = %#v", plan)
	}
	updated, err := provider.Resolve(request, []GoRelease{old, current}, true)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Release.Version != current.Version || updated.Resolution.Installed || !updated.Resolution.Acquire {
		t.Fatalf("explicit Go update resolution = %#v", updated)
	}
	if _, err := provider.Resolve(
		GoProjectRequest{Minimum: "1.27.1", Toolchain: "go1.26.3"},
		[]GoRelease{old, current},
		false,
	); err == nil {
		t.Fatal("toolchain older than the go directive was accepted")
	}
}

func TestGoProviderProvisionsVerifiedOfficialLayout(t *testing.T) {
	store := generationStoreFixture(t)
	bundle := t.TempDir()
	source := filepath.Join(bundle, "go1.27.1.linux-amd64.tar.gz")
	writeTarGzip(t, source, []tarEntry{
		{name: "go/bin/go", data: []byte("#!/bin/sh\necho go\n"), mode: 0755},
		{name: "go/bin/gofmt", data: []byte("#!/bin/sh\necho gofmt\n"), mode: 0755},
		{name: "go/VERSION", data: []byte("go1.27.1\n"), mode: 0644},
	})
	payload, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	release := goRelease("1.27.1", fmt.Sprintf("%x", sha256.Sum256(payload)))
	provider := GoProvider{Store: store}
	plan, err := provider.Resolve(
		GoProjectRequest{Minimum: "1.27.1", Toolchain: "go1.27.1"},
		[]GoRelease{release},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := provider.Provision(t.Context(), plan, source)
	if err != nil {
		t.Fatal(err)
	}
	executables, err := plan.Executables(generation)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go", "gofmt"} {
		info, statErr := os.Stat(executables[name])
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			t.Fatalf("Go executable %s = %v, %v", name, info, statErr)
		}
	}

	if err := os.WriteFile(source, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Provision(t.Context(), plan, source); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered Go archive error = %v", err)
	}
}

func TestGoReleaseRejectsUntrustedIdentity(t *testing.T) {
	release := goRelease("1.27.1", strings.Repeat("c", 64))
	release.URL = "https://example.test/" + release.Filename()
	if err := release.Validate(); err == nil {
		t.Fatal("untrusted Go release URL was accepted")
	}
	release = goRelease("1.27.1", strings.Repeat("c", 64))
	release.SHA256 = strings.Repeat("C", 64)
	if err := release.Validate(); err == nil {
		t.Fatal("non-canonical Go checksum was accepted")
	}
}
