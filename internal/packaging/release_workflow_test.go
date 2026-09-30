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
		"Publish the accepted release after validation.",
		"type: boolean",
		"default: true",
		"if: ${{ inputs.publish }}",
		"actions-up@1.21.0",
		"npm view actions-up version",
		`gh release view "$latest_tag" --json isDraft,isImmutable`,
		"current commit is already published immutably as $latest_tag",
		"current commit has a mutable failed release at $latest_tag; advancing to the next version",
		"./scripts/verify/release-pins.sh metadata",
		"./scripts/verify/release-pins.sh containers",
		"Verify release container pins are current",
		"crazy-max/ghaction-github-runtime@",
		"# v4.0.0",
		"release-contracts:",
		"Verify release contracts and pins",
		"release-inputs:",
		"source-runtime-contracts:",
		"Verify source runtime fixtures",
		"oci-gates:",
		"Verify OCI runtime",
		"Verify Windows WSL appliance",
		"windows-2025",
		".\\scripts\\verify\\update-wsl.ps1 -Attempts 3 -DelaySeconds 10",
		"Parse Windows verification scripts",
		"[void][scriptblock]::Create",
		".\\scripts\\verify\\accept-wsl.ps1",
		"wsl-accept:",
		"windows-provider-acceptance:",
		"Verify Windows provider acceptance",
		"LOKI_WINDOWS_PROVIDER_ACCEPTANCE",
		"TestWindowsProviderAcceptance",
		"- windows-provider-acceptance",
		"source-gates:",
		"LOKI_TEST_RG:",
		"CI-sensitive Go test detail",
		"ubuntu-24.04-arm",
		"./scripts/build/build-release-inputs.sh",
		"pattern: loki-release-inputs-*",
		"merge-multiple: true",
		"Restore release input executable modes",
		"Prepare release candidate",
		"sh ./scripts/verify/prepare-candidate.sh",
		"path: ${{ runner.temp }}/candidate",
		"LOKI_BUILD_CACHE_SCOPE: release-inputs",
		"LOKI_BUILD_CACHE_SCOPE=loki-core",
		"LOKI_BUILD_CACHE_SCOPE=loki-browser",
		"./scripts/build/build-oci.sh",
		"./scripts/build/build-browser-oci.sh",
		"./scripts/build/build-wsl.sh",
		"Require anonymously pullable runtime images",
		"go run ./tools/release/releasebuild",
		"go run ./tools/release/evidencebuild",
		"go run ./tools/release/helperfetch",
		"--catalog \"$GITHUB_WORKSPACE/packaging/windows/connect-helpers.json\"",
		"go run ./tools/release/windowsbuild",
		"--windows-frontend",
		"--connect-helper-catalog",
		"--connect-helper-archive",
		"--connect-helper-license",
		"--connect-helper-notice",
		"--connect-helper-spdx",
		"--wsl-appliance",
		"go run ./tools/release/publishprep",
		"--windows-installer-template",
		"Run CI-sensitive Go tests",
		"CI-sensitive Go test failed",
		"Run deterministic source profile",
		"sh ./scripts/verify/verify-source.sh",
		"Run Go race profile",
		"sh ./scripts/verify/verify-race.sh",
		"Run runner/vault OS permission acceptance",
		"go test -c -o \"$RUNNER_TEMP/execution-permission.test\" ./internal/execution",
		"sudo \"$RUNNER_TEMP/execution-permission.test\" -test.run '^TestLinuxRunnerCanWriteStateButCannotReadVaultKey$' -test.v",
		"Run release OCI authority acceptance",
		"LOKI_OCI_ACCEPTANCE_IMAGE: ${{ needs.build.outputs.core_image }}",
		"Run protected-resource cross-path authority matrix",
		"LOKI_IMAGE: ${{ needs.build.outputs.core_image }}",
		"bash ./scripts/verify/accept-authority-matrix.sh",
		"./scripts/verify/accept-oci-jobs.sh",
		"Run MCP-only project execution acceptance",
		"./scripts/verify/accept-project-execution.sh",
		"Run provider-neutral MCP ingress acceptance",
		"TestProviderNeutralExternalIngressAcceptance",
		"Run provider release contract acceptance",
		"go test ./internal/integrations/github -v -count=1",
		"Run release Compose, browser and signing acceptance",
		`LOKI_SIGNING_KEY_FILE="$key" ./scripts/verify/accept-compose.sh`,
		"./scripts/verify/accept-bootstrap.sh",
		"./scripts/build/build-toolchain-bundle.sh",
		"Create or verify release tag",
		"releaseway/actions@",
		"actions/upload-pages-artifact@",
		"actions/deploy-pages@",
		"https://jinyongp.dev/loki/install.sh",
		"https://jinyongp.dev/loki/install.ps1",
		"loki-wsl-amd64.wsl",
		"loki-install.ps1",
		"Run public source-free installation",
		"verify-public-windows:",
		"Verify public Windows install",
		"Verify published PowerShell installer bytes",
		"Run public Windows source-free installation",
		"irm https://jinyongp.dev/loki/install.ps1 | iex",
		"public Windows frontend release binding does not match",
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

func TestReleaseAcceptanceDomainsAreIndependent(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "\n  accept:\n") {
		t.Fatal("release acceptance must not collapse independent domains into one serial fail-fast job")
	}
	between := func(startKey, endKey string) string {
		t.Helper()
		start := strings.Index(text, "\n  "+startKey+":\n")
		end := strings.Index(text, "\n  "+endKey+":\n")
		if start < 0 || end < 0 || end <= start {
			t.Fatalf("release workflow lacks %s..%s job boundary", startKey, endKey)
		}
		return text[start:end]
	}
	preflight := between("preflight", "release-contracts")
	for _, forbidden := range []string{
		"Run fast release contract tests",
		"Verify GitHub Action pins are current",
		"Verify release dependency pins are current",
	} {
		if strings.Contains(preflight, forbidden) {
			t.Fatalf("candidate identity preflight still serializes independent validation %q", forbidden)
		}
	}
	releaseContracts := between("release-contracts", "release-inputs")
	for _, required := range []string{
		"Run fast release contract tests",
		"Verify GitHub Action pins are current",
		"Verify release dependency pins are current",
		"Verify release container pins are current",
		"continue-on-error: true",
		"Require all release contract and pin checks",
	} {
		if !strings.Contains(releaseContracts, required) {
			t.Errorf("release contract gate lacks %q", required)
		}
	}
	if strings.Contains(between("release-inputs", "source-gates"), "Verify release container pins are current") {
		t.Fatal("release input construction must not be suppressed by independent pin freshness validation")
	}
	race := between("source-race", "build")
	for _, required := range []string{
		"- release-inputs",
		"if: ${{ always() && needs.preflight.result == 'success' }}",
		"name: loki-release-inputs-amd64",
		"LOKI_TEST_RG:",
	} {
		if !strings.Contains(race, required) {
			t.Fatalf("race validation lacks pinned-tool fan-out contract %q", required)
		}
	}
	for _, forbidden := range []string{"- source-gates", "- source-runtime-contracts", "- oci-gates", "- release-contracts"} {
		if strings.Contains(race, forbidden) {
			t.Fatalf("race validation still depends on independent gate %q", forbidden)
		}
	}
	source := between("source-gates", "source-runtime-contracts")
	for _, required := range []string{
		"- release-inputs",
		"if: ${{ always() && needs.preflight.result == 'success' }}",
		"name: loki-release-inputs-amd64",
		"LOKI_TEST_RG:",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("source validation lacks pinned-tool fan-out contract %q", required)
		}
	}
	for _, forbidden := range []string{"- source-gates", "- source-race", "- oci-gates", "- release-contracts"} {
		if strings.Contains(between("build", "wsl-accept"), forbidden) {
			t.Fatalf("candidate build is suppressed by independent validation %q", forbidden)
		}
	}
	if strings.Contains(between("windows-provider-acceptance", "accept-runtime"), "- windows-native") {
		t.Fatal("Windows provider acceptance must run independently of native Windows unit validation")
	}

	type domain struct {
		key      string
		next     string
		required string
	}
	domains := []domain{
		{"accept-runtime", "accept-recovery", "Run MCP-only project execution acceptance"},
		{"accept-recovery", "accept-oci", "Run published release lifecycle recovery acceptance"},
		{"accept-oci", "accept-authority", "Run release OCI authority acceptance"},
		{"accept-authority", "accept-compose", "Run protected-resource cross-path authority matrix"},
		{"accept-compose", "accept-bootstrap", "Run release Compose, browser and signing acceptance"},
		{"accept-bootstrap", "publish", "Run bootstrap acceptance"},
	}
	for _, d := range domains {
		start := strings.Index(text, "\n  "+d.key+":\n")
		end := strings.Index(text, "\n  "+d.next+":\n")
		if start < 0 || end < 0 || end <= start {
			t.Fatalf("release workflow lacks independent %s job boundary", d.key)
		}
		block := text[start:end]
		if !strings.Contains(block, d.required) {
			t.Fatalf("%s job lacks %q", d.key, d.required)
		}
		for _, forbidden := range []string{"- source-gates", "- source-runtime-contracts", "- source-race", "- oci-gates", "- release-contracts"} {
			if strings.Contains(block, forbidden) {
				t.Fatalf("%s job is suppressed by independent validation %q", d.key, forbidden)
			}
		}
	}

	publish := strings.Index(text, "\n  publish:\n")
	if publish < 0 {
		t.Fatal("release workflow lacks publish job")
	}
	publishBlock := text[publish:]
	for _, required := range []string{
		"- release-contracts",
		"- source-gates",
		"- source-runtime-contracts",
		"- source-race",
		"- oci-gates",
		"- windows-native",
		"- accept-runtime",
		"- accept-recovery",
		"- accept-oci",
		"- accept-authority",
		"- accept-compose",
		"- accept-bootstrap",
		"- wsl-accept",
		"- windows-provider-acceptance",
	} {
		if !strings.Contains(publishBlock, required) {
			t.Errorf("publication barrier lacks %q", required)
		}
	}
}

func TestPublicWindowsInstallIsSourceFree(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "\n  verify-public-windows:\n")
	if start < 0 {
		t.Fatal("release workflow lacks public Windows install job")
	}
	job := text[start:]
	for _, required := range []string{
		"runs-on: windows-2025",
		"needs:\n      - preflight\n      - pages",
		"gh release download $env:TAG",
		"https://jinyongp.dev/loki/install.ps1",
		"irm https://jinyongp.dev/loki/install.ps1 | iex",
		"loki.exe",
		"version --json",
		"status --distribution",
		"doctor --distribution",
		"connection show --distribution",
		"connection list --distribution",
		"Run public Windows self-update from previous release",
		"$beforeApplianceRelease",
		"bare self-update mutated appliance release",
		"update prepare --distribution $distribution",
		"explicit appliance prepare did not publish a prepared plan",
		"update apply --distribution $distribution --approve",
		"explicit appliance apply produced",
		`id -eq "openai"`,
		`state -ne "not-configured"`,
		"uninstall --distribution",
	} {
		if !strings.Contains(job, required) {
			t.Errorf("public Windows install job lacks %q", required)
		}
	}
	if strings.Contains(job, "actions/checkout@") {
		t.Fatal("public Windows source-free install job checks out repository source")
	}
}

func TestRetiredWindowsPrecutoverAcceptanceIsRemoved(t *testing.T) {
	root := filepath.Join("..", "..")
	path := filepath.Join(root, "scripts", "verify", "accept-windows-frontend-precutover.ps1")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("retired pre-cutover PowerShell lifecycle acceptance must be removed, stat err=%v", err)
	}
}

func TestProjectExecutionReleaseAcceptanceUsesExactCandidateToolchains(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "verify", "accept-project-execution.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"CORE_IMAGE must be pinned by a sha256 digest",
		"sha256sum -c SHA256SUMS",
		`.artifacts[] | select(.name == "node")`,
		`.artifacts[] | select(.name == "pnpm")`,
		`.artifacts[] | select(.name == "chromium")`,
		`$docker" cp "$container:/opt/loki/bin/devtools" "$devtools"`,
		"io.loki.devtools.$arch.sha256",
		"LOKI_E2E_DEVTOOLS",
		"LOKI_E2E_PNPM",
		"LOKI_E2E_NODE",
		"LOKI_E2E_CHROMIUM",
		"go test ./internal/e2e -run '^TestProjectExecutionContract$' -v -count=1",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("project execution acceptance lacks %q", required)
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
		"DEVTOOLS_VERSION=0.21.0",
		"DEVTOOLS_COMMIT=dec585b4a537f79a656b69261d208073682e1006",
		"GH_VERSION=2.102.0",
		"GH_COMMIT=fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd",
		"RIPGREP_VERSION=15.2.0",
		"RIPGREP_COMMIT=e89fff89ac9af12e8d4ce9d5fd07beb408ca730f",
		"golang:1.27.1-bookworm@sha256:",
		"rust:1.98.1-alpine3.24@sha256:7cc1c22d77d9432f7fe012a70e6d3e555af54c2a6832700ed7d553f1769ae89f",
		"apk add --no-cache git build-base pcre2-dev perl",
		"PCRE2_SYS_STATIC=1",
		"cargo build --locked --profile release-lto --features pcre2",
		`/out/devtools version | grep -q '"version":"0.21.0"'`,
		`/out/gh version | grep -q '^gh version 2\.102\.0 '`,
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

func TestReleasePinVerifierCoversCurrentStableToolchain(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "verify", "release-pins.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"https://go.dev/dl/?mode=json",
		"https://static.rust-lang.org/dist/channel-rust-stable.toml",
		"https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/x86_64/",
		"APKINDEX.tar.gz",
		"jinyongp/devtools",
		"cli/cli",
		"BurntSushi/ripgrep",
		"releaseway/actions",
		"https://download.docker.com/linux/ubuntu/dists/noble/stable/binary-amd64/Packages",
		"latest_package_version docker-ce",
		"latest_package_version docker-compose-plugin",
		"docker/dockerfile:1",
		"alpine/git:latest",
		"docker buildx imagetools inspect",
		"packaging/images/Dockerfile|core",
		"packaging/images/browser.Dockerfile|browser",
		"packaging/release-inputs/Dockerfile|release-inputs",
		"verify_frontend",
		"verify_go_builder",
		"verify_alpine_runtime",
		"release dependency check unavailable",
		"current digest could not be resolved after 3 attempts",
		"while test \"$attempt\" -le 3",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("release pin verifier lacks %q", required)
		}
	}
}

func TestWSLUpdateUsesBoundedRetry(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "verify", "update-wsl.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"& wsl.exe --update --web-download",
		"for ($attempt = 1; $attempt -le $Attempts; $attempt++)",
		"Start-Sleep -Seconds $DelaySeconds",
		"WSL package update attempt $attempt failed",
		"WSL package update unavailable after $Attempts attempts",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("WSL update helper lacks %q", required)
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
		t.Fatal("first-install documentation no longer presents the canonical one-line installer and release-bound bootstrap")
	}
	for _, stale := range []string{"first public release has not been published", "pre-release build"} {
		if strings.Contains(strings.ToLower(firstInstall), stale) {
			t.Fatalf("first-install documentation contains stale release-state copy %q", stale)
		}
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
