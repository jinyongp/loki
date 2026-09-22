package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestS06CanonicalReleaseStructure(t *testing.T) {
	root := filepath.Join("..", "..")

	for _, path := range []string{
		"packaging/images/Dockerfile",
		"packaging/images/browser.Dockerfile",
		"packaging/native/execution-contract.json",
		"packaging/native/systemd/host.target",
		"scripts/build/build-oci.sh",
		"scripts/build/build-candidate.sh",
		"scripts/verify/verify-release.sh",
		"scripts/verify/accept-compose.sh",
		"scripts/maintainer/render-layouts.sh",
		"tools/release/bootstrapbuild/main.go",
		"tools/release/evidencebuild/main.go",
		"tools/release/README.md",
		"docs/first-install.md",
	} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("canonical S06 path %s: %v", path, err)
		}
		if info.IsDir() {
			t.Fatalf("canonical S06 path is unexpectedly a directory: %s", path)
		}
	}

	for _, path := range []string{
		"packaging/container",
		"packaging/go",
		"tools/bootstrapbuild",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		err := filepath.WalkDir(full, func(current string, entry os.DirEntry, walkErr error) error {
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() {
				t.Fatalf("retired S06 path still contains source: %s", current)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("inspect retired S06 path %s: %v", path, err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(root, "scripts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("scripts root entries = %d, want build/verify/maintainer only", len(entries))
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Fatalf("scripts root contains uncategorized file %s", entry.Name())
		}
		switch entry.Name() {
		case "build", "verify", "maintainer":
		default:
			t.Fatalf("scripts root contains unexpected category %s", entry.Name())
		}
	}
}

func TestPublicOneLineInstallerRemainsGated(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, path := range []string{
		"README.md",
		"docs/first-install.md",
		"docs/installation-distribution-plan.md",
	} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		if strings.Contains(body, "curl -fsSL https://jinyongp.dev/loki/install.sh") {
			t.Fatalf("%s advertises the gated public one-line installer", path)
		}
	}
	firstInstall, err := os.ReadFile(filepath.Join(root, "docs", "first-install.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"not published or advertised",
		"loki-bootstrap",
		"./loki-bootstrap",
		"tools/release/bootstrapbuild",
		"does not need a Loki source checkout",
		"you do not need to install them manually",
		"loki host status",
		"loki host connection",
		"--install-prerequisites",
		"A14 release acceptance",
	} {
		if !strings.Contains(string(firstInstall), required) {
			t.Fatalf("first-install documentation lacks %q", required)
		}
	}
}
