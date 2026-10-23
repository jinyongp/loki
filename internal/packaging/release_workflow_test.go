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
			Steps []struct {
				Uses, Run, If string
				With          map[string]string
			}
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
	if strings.Join(needs, ",") != "plan,gate" {
		t.Fatal("publication bypasses final gate or explicit request")
	}
	// A successful gate can have intentionally skipped ancestors in a selective
	// build. Explicit status functions avoid the implicit success() skip cascade;
	// direct dependency checks still prevent publishing failed or absent gates.
	for job, required := range map[string][]string{
		"publish":       {"always()", "inputs.publish", "needs.plan.result == 'success'", "needs.gate.result == 'success'"},
		"pages":         {"always()", "needs.publish.result == 'success'"},
		"verify-public": {"always()", "needs.plan.result == 'success'", "needs.pages.result == 'success'"},
	} {
		for _, guard := range required {
			if !strings.Contains(jobs[job].If, guard) {
				t.Fatalf("%s omits selective publication guard %q", job, guard)
			}
		}
	}
	var assertsPublicDelivery bool
	for _, step := range jobs["summary"].Steps {
		if strings.Contains(step.Run, "os.environ['PUBLISH'] == 'true'") &&
			strings.Contains(step.Run, "value['result'] != 'success'") && strings.Contains(step.Run, "raise SystemExit") {
			assertsPublicDelivery = true
		}
	}
	if !assertsPublicDelivery {
		t.Fatal("requested publication must fail when public delivery is skipped or fails")
	}
	needs = nil
	gate := jobs["gate"]
	if jobs["source-checks"].Needs.Kind != 0 {
		t.Fatal("source checks must start without waiting for release assembly")
	}
	checkedResults := false
	for _, step := range gate.Steps {
		if strings.Contains(step.Run, "release_gate.py results ") {
			checkedResults = true
		}
		if step.With["name"] == "publication-assets" && !checkedResults {
			t.Fatal("required job results must be checked before asset download")
		}
	}
	if !checkedResults {
		t.Fatal("missing early required result check")
	}
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
		if strings.HasPrefix(step.Uses, "releaseway/actions@b6f17f2ffe2c8fd92bdf6be4729e1e56d151bef3") {
			lifecycle = true
			if step.With["upload-concurrency"] != "4" {
				t.Fatal("publisher must use four bounded simultaneous uploads")
			}
			if step.With["notes"] != "standard" || step.With["notes-file"] != "" || step.With["notes-existing"] != "auto" {
				t.Fatal("use Releaseway standard notes and preserve existing immutable release bodies")
			}
		}
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

func TestReleaseTransferScopesPreserveSealingAndPublicComparison(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses, Run, If string
				With          map[string]any
			}
		}
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	writers := map[string]string{}
	for _, step := range workflow.Jobs["assemble"].Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			name, _ := step.With["name"].(string)
			path, _ := step.With["path"].(string)
			writers[name] = path
			if name == "publication-pages" && step.With["include-hidden-files"] != true {
				t.Fatal("Pages artifact must preserve .nojekyll")
			}
		}
	}
	if writers["publication-assets"] != "publication/assets/" || writers["publication-pages"] != "publication/pages/" {
		t.Fatal("release assets and installer pages must have separate transfer payloads")
	}
	for _, name := range []string{"gate", "publish", "verify-public"} {
		var assets, pages, sealedMetadata, inventory, verifier bool
		for _, step := range workflow.Jobs[name].Steps {
			if strings.Contains(step.Run, "release_inventory.py --publication publication") {
				verifier = true
				if name == "gate" && step.If != "${{ !inputs.publish }}" {
					t.Fatal("validation-only gate must verify actual bytes")
				}
			}
			if !strings.HasPrefix(step.Uses, "actions/download-artifact@") {
				continue
			}
			artifact, _ := step.With["name"].(string)
			switch artifact {
			case "publication-assets":
				assets = step.With["path"] == "publication/assets"
				if name == "gate" && step.If != "${{ !inputs.publish }}" {
					t.Fatal("publishing gate must avoid downloading all asset bytes")
				}
			case "publication-metadata":
				inventory = step.With["path"] == "publication"
			case "publication-pages":
				pages = step.With["path"] == "publication/pages"
			case "accepted-publication-metadata":
				sealedMetadata = step.With["path"] == "publication"
			case "publication":
				t.Fatal("job still downloads combined publication payload:", name)
			}
		}
		if name == "verify-public" && (assets || !pages) {
			t.Fatal("public installer comparison must receive only pages")
		}
		if name == "gate" && (!assets || !pages || !inventory || !verifier) {
			t.Fatal("gate must seal inventory and verify validation-only bytes")
		}
		if name == "publish" && (!assets || pages || !sealedMetadata || !verifier) {
			t.Fatal("publisher must receive assets and final sealed metadata")
		}
	}
}

func TestNativeChecksDownloadOnlyConsumedBuildFamilies(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses, If string
				With     map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	for job, families := range map[string][]string{
		"manager-checks": {"manager"}, "browser-checks": {"manager", "browser"}, "full-checks": {"manager", "full"},
	} {
		found := map[string]bool{}
		for _, step := range workflow.Jobs[job].Steps {
			if !strings.HasPrefix(step.Uses, "actions/download-artifact@") {
				continue
			}
			if strings.HasPrefix(step.With["pattern"], "built-") {
				t.Fatal("native checks download unrelated build families:", job)
			}
			name := step.With["name"]
			if !strings.HasPrefix(name, "built-") {
				continue
			}
			family := strings.TrimPrefix(name, "built-${{ matrix.os }}-${{ matrix.arch }}-")
			if found[family] || step.If != "matrix."+family+"_built" || step.With["path"] != "publication/native/"+family {
				t.Fatal("build acquisition must be exact, conditional and isolated:", job, family)
			}
			found[family] = true
		}
		if len(found) != len(families) {
			t.Fatal("wrong build families:", job, found)
		}
		for _, family := range families {
			if !found[family] {
				t.Fatal("missing consumed family:", job, family)
			}
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
