package packaging

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseGateIncludesEveryRequiredLayer(t *testing.T) {
	gate := readPortabilityFile(t, filepath.Join("..", "..", "scripts", "verify", "verify-release.sh"))
	for _, required := range []string{
		"go test ./...",
		"go test -race ./...",
		"go vet ./...",
		"accept-bootstrap.sh",
		"accept-candidate.sh",
		"accept-compose.sh",
		"inspect_image \"$core_image\" Loki",
		"inspect_image \"$browser_image\" 'Loki Browser'",
	} {
		if !strings.Contains(gate, required) {
			t.Errorf("release gate lacks %q", required)
		}
	}
}

func TestDocumentedRepositoryScriptsExist(t *testing.T) {
	root := filepath.Join("..", "..")
	reference := regexp.MustCompile(`\./(scripts/(?:[A-Za-z0-9._-]+/)*[A-Za-z0-9._-]+)`)
	documents := []string{
		filepath.Join(root, "README.md"),
		filepath.Join(root, "docs", "go-candidate-runbook.md"),
		filepath.Join(root, "docs", "self-hosting.md"),
	}
	for _, document := range documents {
		raw, err := os.ReadFile(document)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range reference.FindAllStringSubmatch(string(raw), -1) {
			path := filepath.Join(root, filepath.FromSlash(match[1]))
			info, statErr := os.Stat(path)
			if statErr != nil {
				t.Errorf("%s references missing %s", document, match[1])
				continue
			}
			if info.Mode()&0111 == 0 {
				t.Errorf("%s references non-executable %s", document, match[1])
			}
		}
	}
}

func TestPublicInstallerAdvertisingRemainsGated(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, relative := range []string{
		"README.md",
		"docs/first-install.md",
		"docs/installation-distribution-plan.md",
	} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "curl -fsSL https://jinyongp.dev/loki/install.sh") {
			t.Fatalf("%s advertises the pre-A14 public installer", relative)
		}
	}
}
