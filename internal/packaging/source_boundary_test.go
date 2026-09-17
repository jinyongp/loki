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
	paths := []string{".dockerignore", "compose.yaml", "packaging/container/Dockerfile", "packaging/container/browser.Dockerfile"}
	entries, err := os.ReadDir(filepath.Join(root, "scripts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			paths = append(paths, filepath.Join("scripts", entry.Name()))
		}
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
