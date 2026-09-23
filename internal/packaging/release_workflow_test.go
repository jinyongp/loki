package packaging

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseWorkflowPublishesOnlyAcceptedCandidate(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"workflow_call:",
		"candidate-artifact:",
		"go run ./tools/release/publishprep",
		"releaseway/actions@5b7090184832d6fc92ccee5b3caa0417d3df9235 # v0.1.0",
		"needs: release",
		"environment:",
		"name: github-pages",
		"actions/deploy-pages@368f82528645a54fb793d4d04e342629a3f51346 # v5.0.1",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("release workflow lacks %q", required)
		}
	}
	if strings.Count(text, "ref: ${{ inputs.commit }}") != 2 {
		t.Fatal("release workflow does not pin both checkouts to the accepted commit")
	}
	for _, forbidden := range []string{
		"workflow_dispatch:",
		"push:",
		"pull_request:",
		"releaseway/actions@v",
		"--clobber",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("release workflow contains forbidden publication path %q", forbidden)
		}
	}

	uses := regexp.MustCompile("(?m)^\\s*uses:\\s+([^\\s#]+)").FindAllStringSubmatch(text, -1)
	if len(uses) == 0 {
		t.Fatal("release workflow has no actions")
	}
	fullPin := regexp.MustCompile("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$")
	for _, match := range uses {
		if !fullPin.MatchString(match[1]) {
			t.Fatalf("release workflow action is not pinned to a full commit SHA: %s", match[1])
		}
	}
}

func TestInstallerDocumentationKeepsCanonicalOneLineInstall(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	planRaw, err := os.ReadFile(filepath.Join(root, "docs", "installation-distribution-plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	firstInstallRaw, err := os.ReadFile(filepath.Join(root, "docs", "first-install.md"))
	if err != nil {
		t.Fatal(err)
	}
	plan := string(planRaw)
	firstInstall := string(firstInstallRaw)
	for _, required := range []string{
		"releaseway/actions",
		"jinyongp.dev/loki/install.sh",
		"loki-bootstrap-linux-amd64",
		"release-bound",
	} {
		if !strings.Contains(plan, required) {
			t.Fatalf("installation distribution plan lacks %q", required)
		}
	}
	if !strings.Contains(firstInstall, "curl -fsSL https://jinyongp.dev/loki/install.sh | sh") ||
		!strings.Contains(firstInstall, "loki-bootstrap-linux-amd64") {
		t.Fatal("first-install documentation no longer presents the canonical one-line installer and pre-release fallback")
	}
}

func TestInstallerTemplateIsReleaseBoundAndThin(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "tools", "release", "install.sh.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"release_tag='@@LOKI_RELEASE_TAG@@'",
		"bootstrap_sha256='@@LOKI_BOOTSTRAP_SHA256@@'",
		"loki-bootstrap-linux-amd64",
		"sha256sum -c -",
		"\"$bootstrap\" \"$@\"",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("installer template lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		"/releases/latest/",
		"loki host install",
		"docker ",
		"apt-get ",
		"setfacl ",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("installer template contains lifecycle logic or mutable release selection: %q", forbidden)
		}
	}
}
