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
		"npm view actions-up version",
		"jinyongp/devtools --json tagName",
		"cli/cli --json tagName",
		"BurntSushi/ripgrep --json tagName",
		"releaseway/actions --json tagName",
		"crazy-max/ghaction-github-runtime@",
		"# v4.0.0",
		"release-inputs:",
		"ubuntu-24.04-arm",
		"./scripts/build/build-release-inputs.sh",
		"pattern: loki-release-inputs-*",
		"merge-multiple: true",
		"Restore release input executable modes",
		"Restore candidate executable modes",
		"chmod 0755 candidate/inputs/loki candidate/inputs/loki-bootstrap",
		"LOKI_BUILD_CACHE_SCOPE: release-inputs",
		"LOKI_BUILD_CACHE_SCOPE=loki-core",
		"LOKI_BUILD_CACHE_SCOPE=loki-browser",
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
		"releaseway/actions@",
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

func TestReleaseInputBuilderUsesPinnedUpstreamSource(t *testing.T) {
	root := filepath.Join("..", "..")
	scriptRaw, err := os.ReadFile(filepath.Join(root, "scripts", "build", "build-release-inputs.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dockerfileRaw, err := os.ReadFile(filepath.Join(root, "packaging", "release-inputs", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptRaw)
	dockerfile := string(dockerfileRaw)
	for _, required := range []string{
		"usage: build-release-inputs.sh OUTPUT_DIRECTORY ARCH",
		"packaging/release-inputs/Dockerfile",
		"--platform \"linux/$arch\"",
		`requires a native $arch runner`,
		"--target \"$target\"",
		"--progress=plain",
		"type=local,dest=$destination",
		"::error::release input %s linux/%s source build failed: %s",
		"type=gha,version=2,scope=$scope",
		"type=gha,version=2,mode=max,scope=$scope,ignore-error=true",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("release input builder lacks %q", required)
		}
	}
	for _, required := range []string{
		"DEVTOOLS_VERSION=0.18.0",
		"DEVTOOLS_COMMIT=6d93f0a3c24976a108cf9c4aa374dbe6c467559e",
		"GH_VERSION=2.101.0",
		"GH_COMMIT=0cf1092493af067646fc5f3db9421c6a6ec9c938",
		"RIPGREP_VERSION=15.2.0",
		"RIPGREP_COMMIT=e89fff89ac9af12e8d4ce9d5fd07beb408ca730f",
		"golang:1.27.1-bookworm@sha256:",
		"rust:1.98.1-alpine3.24@sha256:c913be57168b9240b86f373f94060152a2e09ea16a72e0801a02ee3a262ca446",
		"apk add --no-cache git build-base pcre2-dev perl",
		"PCRE2_SYS_STATIC=1",
		"cargo build --locked --profile release-lto --features pcre2",
		`/out/devtools version | grep -q '"version":"0.18.0"'`,
		`/out/gh version | grep -q '^gh version 2\.101\.0 '`,
		`/src/ripgrep/target/release-lto/rg --version | grep -q '^ripgrep 15\.2\.0 '`,
		"/src/ripgrep/target/release-lto/rg",
		"AS go-export",
		"AS ripgrep-export",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("release-input Dockerfile lacks %q", required)
		}
	}
	for _, forbidden := range []string{"releases/download", "gh release download", "curl "} {
		if strings.Contains(script, forbidden) || strings.Contains(dockerfile, forbidden) {
			t.Fatalf("release input build depends on binary release downloads: %q", forbidden)
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
