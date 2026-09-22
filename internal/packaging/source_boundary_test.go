package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These checks also pass after the retired implementation is deleted: current
// packaging must not require the archive to be present.
func TestRetiredPythonEntrypointsAreOutsideCurrentSource(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, name := range []string{
		"pyproject.toml", "src", "tests", "browser_sidecar", "systemd",
		"config/loki-mcp.toml", "scripts/install-loki-mcp.sh",
		"scripts/verify-and-deploy-loki.sh", "scripts/backup-python-deployment.sh",
		"internal/secret/testdata/python_reference.py",
	} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("retired Python path must not be a current entrypoint: %s (%v)", name, err)
		}
	}
}

func TestCurrentPackagingDoesNotDependOnPythonArchive(t *testing.T) {
	root := filepath.Join("..", "..")
	paths := []string{".dockerignore", "compose.yaml", "packaging/images/Dockerfile", "packaging/images/browser.Dockerfile"}
	err := filepath.WalkDir(filepath.Join(root, "scripts"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range paths {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"legacy/python", "loki_mcp", "browser_sidecar"} {
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("current packaging %s depends on %s", name, forbidden)
			}
		}
	}
}

func TestCanonicalReleasePackagingPaths(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, relative := range []string{
		"packaging/images/Dockerfile",
		"packaging/images/browser.Dockerfile",
		"packaging/native/execution-contract.json",
		"packaging/native/systemd/loki-go.target",
		"scripts/build/build-oci.sh",
		"scripts/verify/verify-release.sh",
		"scripts/maintainer/lifecycle.sh",
		"tools/release/bootstrapbuild/main.go",
		"docs/first-install.md",
	} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil || info.IsDir() {
			t.Fatalf("canonical release path %s: %v", relative, err)
		}
	}
	for _, obsolete := range []string{
		"packaging/container/Dockerfile",
		"packaging/go/execution-contract.json",
		"scripts/build-oci.sh",
		"scripts/accept-compose.sh",
		"scripts/lifecycle.sh",
		"tools/bootstrapbuild/main.go",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(obsolete))); !os.IsNotExist(err) {
			t.Fatalf("obsolete release path still exists: %s (%v)", obsolete, err)
		}
	}
}
