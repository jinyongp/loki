package packaging

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseGateIncludesEveryRequiredLayer(t *testing.T) {
	gate := readPortabilityFile(t, filepath.Join("..", "..", "scripts", "verify-loki-release.sh"))
	for _, required := range []string{
		"go test ./...",
		"go test -race ./...",
		"go vet ./...",
		"accept-loki-go-candidate.sh",
		"accept-loki-compose.sh",
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
	reference := regexp.MustCompile(`\./(scripts/[A-Za-z0-9._-]+)`)
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
