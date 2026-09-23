package packaging

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseWorkflowAutomatesBuildAcceptanceAndPublication(t *testing.T) {
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
		"workflow_dispatch:",
		"bump:",
		"actions-up@1.20.1",
		"./scripts/build/fetch-release-inputs.sh",
		"./scripts/build/build-oci.sh",
		"./scripts/build/build-browser-oci.sh",
		"Require anonymously pullable runtime images",
		"go run ./tools/release/releasebuild",
		"go run ./tools/release/evidencebuild",
		"go run ./tools/release/publishprep",
		"./scripts/verify/accept-oci-jobs.sh",
		"./scripts/verify/accept-compose.sh",
		"./scripts/verify/accept-bootstrap.sh",
		"./scripts/build/build-toolchain-bundle.sh",
		"Create or verify release tag",
		"releaseway/actions@078be0809c2db65ab44788000e56d0baa1c1c02a # v0.1.3",
		"actions/upload-pages-artifact@",
		"actions/deploy-pages@",
		"https://jinyongp.dev/loki/install.sh",
		"Run public source-free installation",
		"--install-prerequisites",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("release workflow lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		"workflow_call:",
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

func TestReleaseInputFetcherUsesPublicPinnedArtifacts(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "build", "fetch-release-inputs.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"devtools_version=0.18.0",
		"ripgrep_version=15.2.0",
		"gh_version=2.101.0",
		"curl --fail --location --retry 3 --retry-all-errors",
		"sha256sum -c",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("release input fetcher lacks %q", required)
		}
	}
	for _, forbidden := range []string{"gh release download", "GH_TOKEN"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("release input fetcher depends on cross-repository GitHub auth: %q", forbidden)
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
