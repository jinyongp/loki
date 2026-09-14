package toolchain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "go", "toolchain-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.AptPackages) < 20 || len(manifest.Artifacts) != 4 {
		t.Fatalf("manifest contents = %d packages, %d artifacts", len(manifest.AptPackages), len(manifest.Artifacts))
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
