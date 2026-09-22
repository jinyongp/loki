package toolchain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "native", "toolchain-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.AptPackages) < 20 || len(manifest.Artifacts) != 6 {
		t.Fatalf("manifest contents = %d packages, %d artifacts", len(manifest.AptPackages), len(manifest.Artifacts))
	}
	artifacts := make(map[string]Artifact, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		artifacts[artifact.Name] = artifact
	}
	for name, version := range map[string]string{
		"gh":   "2.101.0",
		"go":   "1.27.1",
		"node": "26.9.0",
		"pnpm": "12.5.1",
	} {
		if got := artifacts[name].Version; got != version {
			t.Errorf("%s version = %q, want %q", name, got, version)
		}
	}
	if _, ok := artifacts["node"].Links["corepack"]; ok {
		t.Fatal("Node 26 manifest must not depend on removed bundled Corepack")
	}
	if pnpm := artifacts["pnpm"]; pnpm.Format != "tar.gz" || pnpm.Links["pnpm"] != "pnpm" {
		t.Fatalf("pnpm 12 artifact contract = %#v", pnpm)
	}
}

func TestManifestRejectsUnsafeInputs(t *testing.T) {
	for _, raw := range []string{
		`{"version":2,"platform":{"id":"ubuntu","version":"24.04","arch":"amd64"},"apt_packages":[{"name":"git","version":"1"}],"artifacts":[{"name":"go","version":"1","filename":"go.tgz","url":"https://go.dev/go.tgz","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","format":"tar.gz","install_path":"/opt/loki/toolchain/go/1","strip_components":1}]}`,
		`{"version":1,"platform":{"id":"ubuntu","version":"24.04","arch":"amd64"},"apt_packages":[{"name":"git","version":"1"}],"artifacts":[{"name":"go","version":"1","filename":"../go.tgz","url":"http://go.dev/go.tgz","sha256":"bad","format":"tar.gz","install_path":"/usr/local/go","strip_components":1}]}`,
		`{"version":1,"platform":{"id":"ubuntu","version":"24.04","arch":"amd64"},"apt_packages":[{"name":"git","version":"1"}],"artifacts":[],"unknown":true}`,
	} {
		if _, err := LoadManifest([]byte(raw)); err == nil {
			t.Fatalf("unsafe manifest accepted: %s", raw)
		}
	}
}
