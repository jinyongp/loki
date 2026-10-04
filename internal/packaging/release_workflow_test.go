package packaging

import (
	"go.yaml.in/yaml/v3"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSelectiveReleasePublicationNeedsExactFinalGate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs yaml.Node
			If    string
			Steps []struct{ Uses, Run string }
		}
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	jobs := workflow.Jobs
	for _, name := range []string{"plan", "manager-build", "browser-build", "full-build", "assemble", "source-checks", "manager-checks", "browser-checks", "full-checks", "gate", "publish", "pages", "verify-public"} {
		if _, ok := jobs[name]; !ok {
			t.Fatal("missing required release job:", name)
		}
	}
	var needs []string
	publish := jobs["publish"]
	if err := publish.Needs.Decode(&needs); err != nil {
		t.Fatal(err)
	}
	if strings.Join(needs, ",") != "plan,gate" || jobs["publish"].If != "inputs.publish" {
		t.Fatal("publication bypasses final gate or explicit request")
	}
	needs = nil
	gate := jobs["gate"]
	if err := gate.Needs.Decode(&needs); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"source-checks", "manager-checks", "browser-checks", "full-checks"} {
		if !strings.Contains(","+strings.Join(needs, ",")+",", ","+name+",") {
			t.Fatal("gate omits required checks:", name)
		}
	}
	var lifecycle bool
	for _, step := range jobs["publish"].Steps {
		lifecycle = lifecycle || strings.HasPrefix(step.Uses, "releaseway/actions@22219bebc51a4127c6dffd9e79706f08cdd678dc")
		if strings.Contains(step.Run, "gh release create") || strings.Contains(step.Run, "skopeo copy") {
			t.Fatal("publisher bypasses Releaseway or duplicates OCI transfer")
		}
	}
	if !lifecycle {
		t.Fatal("release lifecycle must use pinned Releaseway")
	}
	for _, name := range []string{"native-manager.yml", "native-tools.yml", "reviewed-browser-inputs.yml", "reviewed-full-inputs.yml", "publish.yml", "publish-bootstrap.yml"} {
		if _, err := os.Stat(filepath.Join("..", "..", ".github", "workflows", name)); !os.IsNotExist(err) {
			t.Fatal("separate release entry still exists:", name)
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
		!strings.Contains(firstInstall, "SHA-256") {
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
