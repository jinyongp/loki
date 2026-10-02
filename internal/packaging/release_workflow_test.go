package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestReleaseDependencyUpdatesAreOptionalAndContractGateIsRequired(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On struct {
			Dispatch struct {
				Inputs map[string]struct {
					Default any
					Type    string
				}
			} `yaml:"workflow_dispatch"`
		}
		Jobs map[string]struct {
			Steps []struct {
				ID, If, Run string
			}
		}
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	input, ok := workflow.On.Dispatch.Inputs["check_updates"]
	if !ok || input.Default != false || input.Type != "boolean" {
		t.Fatal("dependency update checks must be an explicit opt-in")
	}
	checks := map[string]bool{"action_pins": false, "metadata_pins": false, "container_pins": false}
	var gate string
	for _, step := range workflow.Jobs["release-contracts"].Steps {
		if _, ok := checks[step.ID]; ok {
			checks[step.ID] = step.If == "${{ inputs.check_updates }}"
		}
		if strings.Contains(step.Run, "steps.contract_tests.outcome") {
			if step.If != "${{ always() }}" {
				t.Fatal("contract result gate must run even after a failed step")
			}
			gate = step.Run
		}
	}
	for id, optional := range checks {
		if !optional {
			t.Errorf("%s must follow the dependency update opt-in", id)
		}
	}
	if gate == "" {
		t.Fatal("release contract result gate is missing")
	}
	for _, test := range []struct {
		name, updates, contracts, pins string
		pass                           bool
	}{
		{"updates-skipped", "false", "success", "skipped", true},
		{"contracts-failed", "false", "failure", "skipped", false},
		{"updates-passed", "true", "success", "success", true},
		{"updates-failed", "true", "success", "failure", false},
		{"updates-skipped-when-required", "true", "success", "skipped", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			script := strings.NewReplacer(
				"${{ inputs.check_updates }}", test.updates,
				"${{ steps.contract_tests.outcome }}", test.contracts,
				"${{ steps.action_pins.outcome }}", test.pins,
				"${{ steps.metadata_pins.outcome }}", test.pins,
				"${{ steps.container_pins.outcome }}", test.pins,
			).Replace(gate)
			output, err := exec.Command("bash", "-c", script).CombinedOutput()
			if (err == nil) != test.pass {
				t.Fatalf("gate pass=%v, want %v: %v\n%s", err == nil, test.pass, err, output)
			}
		})
	}
}

type releaseWorkflowJob struct {
	Needs    yaml.Node
	Strategy struct {
		FailFast *bool `yaml:"fail-fast"`
		Matrix   struct {
			Scenario []string
		}
	}
	If    string
	Steps []struct {
		ID, Name, Uses, Run, If string
		Env                     map[string]string
		With                    map[string]string
	}
}

func TestReleaseWSLScenariosRemainRequiredForPublication(t *testing.T) {
	jobs := releaseWorkflowJobs(t)
	job := jobs["wsl-accept"]
	if job.Strategy.FailFast == nil || *job.Strategy.FailFast {
		t.Fatal("WSL failures must not cancel independent scenario results")
	}
	if strings.Join(job.Strategy.Matrix.Scenario, ",") != "integrations,migration,recovery" {
		t.Fatal("WSL release acceptance must exercise all three scenario chains")
	}
	var invocation, routingCheck bool
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "accept-wsl.ps1") {
			invocation = strings.Contains(step.Run, `-Scenario "${{ matrix.scenario }}"`) && step.If == ""
		}
	}
	for _, step := range jobs["windows-native"].Steps {
		routingCheck = routingCheck || strings.Contains(step.Run, "test-wsl-scenarios.ps1")
	}
	if !invocation || !routingCheck {
		t.Fatal("WSL matrix must pass its selected scenario and validate check routing")
	}
	for _, dependency := range releaseJobNeeds(t, jobs["publish"]) {
		if dependency == "wsl-accept" {
			return
		}
	}
	t.Fatal("publication must wait for every WSL scenario")
}

func TestReleaseDraftUsesVerifiedTagAndPreservesPublisherChecks(t *testing.T) {
	var script string
	var tagVerified bool
	for _, step := range releaseWorkflowJobs(t)["publish"].Steps {
		if strings.Contains(step.Run, `sh ./scripts/maintainer/publish-tag.sh "$TAG" "$GITHUB_SHA"`) {
			tagVerified = true
		}
		if step.ID == "release_draft" {
			if !tagVerified {
				t.Fatal("draft creation must follow accepted-commit tag verification")
			}
			script = step.Run
		}
		if step.ID == "release" && (script == "" || !strings.HasPrefix(step.Uses, "releaseway/actions@")) {
			t.Fatal("verified draft must still pass immutable publisher checks")
		}
	}
	if script == "" || !strings.Contains(script, `--draft --verify-tag --target "$GITHUB_REF_NAME"`) {
		t.Fatal("draft creation must use the verified tag and source branch reference")
	}
	if runtime.GOOS == "windows" {
		t.Skip("release draft creation executes on the Linux publication runner")
	}
	for _, state := range []string{"existing", "missing", "denied"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			fake := `#!/usr/bin/env bash
set -eu
if test "$2" = view; then test "$DRAFT_STATE" = existing; exit; fi
printf '%s\n' "$@" > "$DRAFT_CALL"
test "$DRAFT_STATE" != denied
`
			if err := os.WriteFile(filepath.Join(root, "gh"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}
			call := filepath.Join(root, "call")
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"),
				"DRAFT_STATE="+state, "DRAFT_CALL="+call, "TAG=v0.0.1", "GITHUB_REF_NAME=main", "GITHUB_REPOSITORY=fixture/repo")
			output, err := cmd.CombinedOutput()
			if (err == nil) != (state != "denied") {
				t.Fatalf("state=%s: %v\n%s", state, err, output)
			}
			args, readErr := os.ReadFile(call)
			if state == "existing" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("existing release was changed: %q, error=%v", args, readErr)
				}
			} else if readErr != nil || string(args) != "release\ncreate\nv0.0.1\n--repo\nfixture/repo\n--draft\n--verify-tag\n--target\nmain\n--title\nv0.0.1\n--notes-file\npublication/assets/loki-release-notes.md\n" {
				t.Fatalf("draft creation arguments=%q, error=%v", args, readErr)
			}
		})
	}
}

func releaseWorkflowJobs(t *testing.T) map[string]releaseWorkflowJob {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On   map[string]yaml.Node
		Jobs map[string]releaseWorkflowJob
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	if _, manual := workflow.On["workflow_dispatch"]; !manual || len(workflow.On) != 1 {
		t.Fatal("release workflow must have one manual trigger")
	}
	return workflow.Jobs
}

func releaseJobNeeds(t *testing.T, job releaseWorkflowJob) []string {
	t.Helper()
	switch job.Needs.Kind {
	case 0:
		return nil
	case yaml.ScalarNode:
		return []string{job.Needs.Value}
	case yaml.SequenceNode:
		var needs []string
		if err := job.Needs.Decode(&needs); err != nil {
			t.Fatal(err)
		}
		return needs
	default:
		t.Fatal("workflow needs must be a job name or list")
		return nil
	}
}

func TestReleaseWorkflowAutomatesBuildAcceptanceAndPublication(t *testing.T) {
	jobs := releaseWorkflowJobs(t)
	fullPin := regexp.MustCompile("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$")
	for name, job := range jobs {
		if len(job.Steps) == 0 {
			t.Errorf("release job %s has no steps", name)
		}
		for _, dependency := range releaseJobNeeds(t, job) {
			if _, ok := jobs[dependency]; !ok {
				t.Errorf("%s needs unknown job %s", name, dependency)
			}
		}
		for _, step := range job.Steps {
			if step.Uses != "" && !fullPin.MatchString(step.Uses) {
				t.Errorf("%s action is not pinned to a source commit: %s", name, step.Uses)
			}
			if strings.Contains(step.Run, "--clobber") {
				t.Errorf("%s can overwrite release artifacts", name)
			}
		}
	}
	for name, command := range map[string]string{
		"source-gates":     "scripts/verify/verify-source.sh",
		"source-race":      "scripts/verify/verify-race.sh",
		"oci-gates":        "scripts/verify/accept-oci-jobs.sh",
		"accept-oci":       "scripts/verify/accept-oci-jobs.sh",
		"accept-authority": "scripts/verify/accept-authority-matrix.sh",
		"accept-runtime":   "scripts/verify/accept-project-execution.sh",
		"accept-compose":   "scripts/verify/accept-compose.sh",
		"accept-bootstrap": "scripts/verify/accept-bootstrap.sh",
		"build":            "tools/release/releasebuild",
		"publish":          "tools/release/publishprep",
	} {
		found := false
		for _, step := range jobs[name].Steps {
			found = found || strings.Contains(step.Run, command)
		}
		if !found {
			t.Errorf("%s lacks its validation or build command %s", name, command)
		}
	}
	if jobs["publish"].If != "${{ inputs.publish }}" {
		t.Fatal("publication must follow the operator's publish choice")
	}
	found := false
	for _, step := range jobs["accept-oci"].Steps {
		found = found || step.Env["LOKI_OCI_ACCEPTANCE_IMAGE"] == "${{ needs.build.outputs.core_image }}"
	}
	if !found {
		t.Fatal("OCI acceptance must exercise the exact candidate core image")
	}
}

func TestReleaseAcceptanceDomainsAreIndependent(t *testing.T) {
	jobs := releaseWorkflowJobs(t)
	contains := func(values []string, wanted string) bool {
		for _, value := range values {
			if value == wanted {
				return true
			}
		}
		return false
	}
	checks := []string{
		"release-contracts", "source-gates", "source-runtime-contracts", "source-race",
		"oci-gates", "windows-native", "wsl-accept", "windows-provider-acceptance",
		"accept-runtime", "accept-recovery", "accept-oci", "accept-authority",
		"accept-compose", "accept-bootstrap",
	}
	for _, name := range append(append([]string{}, checks...), "build") {
		job, ok := jobs[name]
		if !ok {
			t.Fatalf("release job %s is missing", name)
		}
		for _, dependency := range releaseJobNeeds(t, job) {
			if contains(checks, dependency) {
				t.Errorf("%s is blocked by independent validation %s", name, dependency)
			}
		}
	}
	publishNeeds := releaseJobNeeds(t, jobs["publish"])
	for _, required := range append([]string{"preflight", "build"}, checks...) {
		if !contains(publishNeeds, required) {
			t.Errorf("publication barrier lacks %s", required)
		}
	}
	for _, name := range []string{"source-gates", "source-race", "source-runtime-contracts"} {
		if !contains(releaseJobNeeds(t, jobs[name]), "release-inputs") {
			t.Errorf("%s lacks its pinned tool fixture", name)
		}
	}
	for _, name := range []string{
		"accept-runtime", "accept-recovery", "accept-oci", "accept-authority",
		"accept-compose", "accept-bootstrap", "wsl-accept", "windows-provider-acceptance",
	} {
		if !contains(releaseJobNeeds(t, jobs[name]), "build") {
			t.Errorf("%s lacks the immutable candidate prerequisite", name)
		}
	}
	for _, name := range []string{"pages", "verify-public", "verify-public-windows"} {
		if _, ok := jobs[name]; !ok {
			t.Errorf("release completion job %s is missing", name)
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
		"update --all --distribution $distribution",
		"combined frontend and appliance update failed",
		"combined update produced",
		"$afterRepeat = $afterRepeatRaw | ConvertFrom-Json -AsHashtable",
		"$null -ne $afterRepeat['prepared']",
		"repeated combined update changed the current appliance generation or prepared another update",
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
	for _, name := range []string{"DEVTOOLS", "GH", "RIPGREP"} {
		if !regexp.MustCompile(`(?m)^ARG `+name+`_VERSION=[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(dockerfile) ||
			!regexp.MustCompile(`(?m)^ARG `+name+`_COMMIT=[0-9a-f]{40}$`).MatchString(dockerfile) {
			t.Errorf("release input %s needs an explicit version and source commit", name)
		}
	}
	for _, image := range []string{"golang", "rust"} {
		if !regexp.MustCompile(image + `:[^\s@]+@sha256:[0-9a-f]{64}\b`).MatchString(dockerfile) {
			t.Errorf("release input %s builder needs an immutable image", image)
		}
	}
	for _, required := range []string{
		"apk add --no-cache git build-base pcre2-dev perl",
		"PCRE2_SYS_STATIC=1",
		"cargo build --locked --profile release-lto --features pcre2",
		"/out/devtools version",
		"/out/gh version",
		"/src/ripgrep/target/release-lto/rg --version",
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
		"Start-Sleep -Seconds $delay",
		"Test-WSLReady",
		"Test-TransientWSLDownloadError $output",
		"external download unavailable after $Attempts attempts",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("WSL update helper lacks %q", required)
		}
	}
	jobs := releaseWorkflowJobs(t)
	for job, invocation := range map[string]string{
		"wsl-accept":            `.\scripts\verify\update-wsl.ps1`,
		"verify-public-windows": `& "$env:RUNNER_TEMP\wsl-ci-preparation\update-wsl.ps1"`,
	} {
		prepared := false
		for _, step := range jobs[job].Steps {
			prepared = prepared || step.Run == invocation
			if strings.Contains(step.Run, "wsl.exe --update") {
				t.Fatalf("%s bypasses the shared WSL preparation helper", job)
			}
		}
		if !prepared {
			t.Fatalf("%s does not prepare the WSL environment", job)
		}
	}
	var uploaded, downloaded bool
	for _, step := range jobs["publish"].Steps {
		uploaded = uploaded || (strings.HasPrefix(step.Uses, "actions/upload-artifact@") &&
			step.With["name"] == "wsl-ci-preparation-${{ github.sha }}" && step.With["path"] == "scripts/verify/update-wsl.ps1" && step.With["overwrite"] == "true")
	}
	for _, step := range jobs["verify-public-windows"].Steps {
		downloaded = downloaded || (strings.HasPrefix(step.Uses, "actions/download-artifact@") &&
			step.With["name"] == "wsl-ci-preparation-${{ github.sha }}" && step.With["path"] == `${{ runner.temp }}\wsl-ci-preparation`)
	}
	if !uploaded || !downloaded {
		t.Fatal("public Windows gate does not receive the accepted helper artifact")
	}
	checked := false
	for _, step := range jobs["windows-native"].Steps {
		checked = checked || strings.Contains(step.Run, "test-update-wsl.ps1")
	}
	if !checked {
		t.Fatal("native Windows gate does not run WSL preparation regression tests")
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
