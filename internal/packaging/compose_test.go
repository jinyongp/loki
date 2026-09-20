package packaging

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Configs  map[string]composeSource  `yaml:"configs"`
	Secrets  map[string]composeSource  `yaml:"secrets"`
	Networks map[string]struct {
		Internal bool `yaml:"internal"`
	} `yaml:"networks"`
}

type composeSource struct {
	File string `yaml:"file"`
}

type composeService struct {
	Image       string   `yaml:"image"`
	Command     []string `yaml:"command"`
	User        string   `yaml:"user"`
	Networks    []string `yaml:"networks"`
	Ports       []string `yaml:"ports"`
	Volumes     []string `yaml:"volumes"`
	Secrets     []any    `yaml:"secrets"`
	Configs     []any    `yaml:"configs"`
	Tmpfs       []string `yaml:"tmpfs"`
	ReadOnly    bool     `yaml:"read_only"`
	CapDrop     []string `yaml:"cap_drop"`
	CapAdd      []string `yaml:"cap_add"`
	Security    []string `yaml:"security_opt"`
	NetworkMode string   `yaml:"network_mode"`
	Profiles    []string `yaml:"profiles"`
	Healthcheck struct {
		Test []string `yaml:"test"`
	} `yaml:"healthcheck"`
	DependsOn map[string]struct {
		Condition string `yaml:"condition"`
	} `yaml:"depends_on"`
}

func TestComposeDefinesIsolatedCoreTopology(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var compose composeFile
	if err = yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prepare", "egress", "launcher", "executor", "runtime", "mcp"} {
		service, ok := compose.Services[name]
		if !ok {
			t.Fatalf("missing service %q", name)
		}
		if !service.ReadOnly || !slices.Contains(service.CapDrop, "ALL") || !slices.Contains(service.Security, "no-new-privileges:true") {
			t.Errorf("%s container hardening is incomplete", name)
		}
	}
	if !compose.Networks["private"].Internal || compose.Networks["outbound"].Internal {
		t.Fatal("network boundary is invalid")
	}
	if compose.Services["prepare"].NetworkMode != "none" {
		t.Fatal("prepare service has network access")
	}
	launcher := compose.Services["launcher"]
	executor := compose.Services["executor"]
	if launcher.NetworkMode != "none" || executor.NetworkMode != "none" ||
		len(launcher.Networks) != 0 || len(executor.Networks) != 0 ||
		len(launcher.Ports) != 0 || len(executor.Ports) != 0 {
		t.Fatal("Job control roles received network or published-port authority")
	}
	for _, destination := range []string{"/var/lib/loki/launcher", "/run/loki/launcher", "/run/docker.sock"} {
		if !hasMount(launcher.Volumes, destination) {
			t.Errorf("launcher missing mount %s", destination)
		}
	}
	for _, destination := range []string{"/run/loki/launcher", "/run/loki/executor"} {
		if !hasMount(executor.Volumes, destination) {
			t.Errorf("executor missing socket mount %s", destination)
		}
	}
	for _, destination := range []string{"/run/docker.sock", "/workspace", "/var/lib/loki/runtime"} {
		if hasMount(executor.Volumes, destination) {
			t.Errorf("executor received forbidden mount %s", destination)
		}
	}
	if !hasMount(compose.Services["mcp"].Volumes, "/run/loki/executor") ||
		hasMount(compose.Services["mcp"].Volumes, "/run/loki/launcher") ||
		hasMount(compose.Services["mcp"].Volumes, "/run/docker.sock") {
		t.Fatal("MCP does not preserve the executor-only Job authority boundary")
	}
	for name, service := range compose.Services {
		if hasMount(service.Volumes, "/run/docker.sock") != (name == "launcher") {
			t.Errorf("%s Docker socket authority = %v", name, hasMount(service.Volumes, "/run/docker.sock"))
		}
	}
	if got := compose.Services["egress"].Networks; !slices.Equal(got, []string{"private", "outbound"}) {
		t.Fatalf("egress networks = %v", got)
	}
	if got := compose.Services["egress"].Ports; !slices.Equal(got, []string{"127.0.0.1:18765:18765"}) {
		t.Fatalf("host MCP port = %v", got)
	}
	if !slices.Contains(compose.Services["egress"].Command, "mcp:18765") {
		t.Fatal("egress does not transparently forward the host MCP port")
	}
	for _, name := range []string{"runtime", "mcp"} {
		service := compose.Services[name]
		if !slices.Equal(service.Networks, []string{"private"}) || len(service.Ports) != 0 {
			t.Errorf("%s bypasses the private network", name)
		}
	}
	if !slices.Equal(secretNames(compose.Services["mcp"].Secrets), []string{"mcp_token"}) {
		t.Fatal("MCP token is not a read-only Compose secret")
	}
	if !slices.Equal(secretNames(compose.Services["runtime"].Secrets), []string{"github_app_private_key"}) ||
		!slices.Equal(secretNames(compose.Services["runtime"].Configs), []string{"github_config"}) ||
		!slices.Equal(secretNames(compose.Services["launcher"].Configs), []string{"github_config"}) ||
		!slices.Equal(secretNames(compose.Services["executor"].Configs), []string{"github_config"}) ||
		!slices.Equal(secretNames(compose.Services["mcp"].Configs), []string{"github_config"}) {
		t.Fatal("GitHub config and private key injection boundary is invalid")
	}
	if compose.Configs["github_config"].File != "${LOKI_GITHUB_CONFIG_FILE:-./config/github.compose.toml}" ||
		compose.Secrets["github_app_private_key"].File != "${LOKI_GITHUB_PRIVATE_KEY_FILE:-/dev/null}" {
		t.Fatal("GitHub Compose sources are invalid")
	}
	if !slices.Contains(compose.Services["runtime"].Tmpfs, "/run/loki-private:uid=0,gid=0,mode=0700") {
		t.Fatal("GitHub private runtime tmpfs is missing")
	}
	if !slices.Contains(compose.Services["runtime"].Tmpfs, "/var/tmp/loki/github:uid=0,gid=10001,mode=0710") {
		t.Fatal("GitHub command temporary directory is missing")
	}
	if !slices.Contains(compose.Services["runtime"].Command, "--github-private-key-file") ||
		!slices.Contains(compose.Services["runtime"].Command, "/run/loki-private/github-app-private-key") ||
		secretTarget(compose.Services["runtime"].Secrets, "github_app_private_key") != "/run/loki-private/github-app-private-key" ||
		secretTarget(compose.Services["runtime"].Configs, "github_config") != "/etc/loki/github.toml" ||
		secretTarget(compose.Services["mcp"].Configs, "github_config") != "/etc/loki/github.toml" ||
		!slices.Contains(compose.Services["runtime"].Command, "--github-config") ||
		!slices.Contains(compose.Services["mcp"].Command, "--github-config") {
		t.Fatal("GitHub Compose arguments are incomplete")
	}
	if hasMount(compose.Services["mcp"].Volumes, "/var/lib/loki/runtime") || !hasMount(compose.Services["runtime"].Volumes, "/var/lib/loki/runtime") {
		t.Fatal("runtime vault mount is not isolated")
	}
	if compose.Services["runtime"].DependsOn["egress"].Condition != "service_healthy" ||
		compose.Services["executor"].DependsOn["launcher"].Condition != "service_healthy" ||
		compose.Services["mcp"].DependsOn["runtime"].Condition != "service_healthy" ||
		compose.Services["mcp"].DependsOn["executor"].Condition != "service_healthy" {
		t.Fatal("core readiness order is incomplete")
	}
	for _, name := range []string{"runtime", "mcp"} {
		if !slices.Contains(compose.Services[name].Healthcheck.Test, "--unix") {
			t.Errorf("%s healthcheck does not connect to its Unix dependency", name)
		}
	}
}

func hasMount(mounts []string, destination string) bool {
	for _, mount := range mounts {
		if strings.HasSuffix(mount, ":"+destination) || mount == destination {
			return true
		}
	}
	return false
}

func secretNames(values []any) []string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		switch value := value.(type) {
		case string:
			names = append(names, value)
		case map[string]any:
			if source, ok := value["source"].(string); ok {
				names = append(names, source)
			}
		}
	}
	return names
}

func secretTarget(values []any, source string) string {
	for _, value := range values {
		entry, ok := value.(map[string]any)
		if ok && entry["source"] == source {
			target, _ := entry["target"].(string)
			return target
		}
	}
	return ""
}

func TestComposeSigningProfileIsPrivateAndOptional(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var compose composeFile
	if err = yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	signing := compose.Services["signing"]
	if !slices.Equal(signing.Profiles, []string{"signing"}) || signing.NetworkMode != "none" ||
		!signing.ReadOnly || !slices.Equal(signing.CapDrop, []string{"ALL"}) || !slices.Equal(signing.CapAdd, []string{"CHOWN"}) {
		t.Fatalf("signing hardening: %#v", signing)
	}
	for _, destination := range []string{"/var/lib/loki/signing", "/run/loki"} {
		if !hasMount(signing.Volumes, destination) {
			t.Errorf("missing signing mount %s", destination)
		}
	}
	if hasMount(signing.Volumes, "/workspace") || hasMount(signing.Volumes, "/var/lib/loki/runtime") ||
		!strings.Contains(strings.Join(signing.Volumes, "\n"), "/var/lib/loki/signing/key:ro") {
		t.Fatal("signing mount boundary is invalid")
	}
	if !slices.Contains(signing.Healthcheck.Test, "test -S /run/loki/signing/agent.sock") {
		t.Fatal("signing healthcheck does not verify public socket")
	}
	if _, ok := compose.Services["runtime"].DependsOn["signing"]; ok {
		t.Fatal("core runtime depends on optional signing")
	}
}

func TestComposeBrowserProfileSeparatesChromiumAndEgress(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var compose composeFile
	if err = yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	browser := compose.Services["browser"]
	proxy := compose.Services["browser-proxy"]
	if !slices.Equal(browser.Profiles, []string{"browser"}) || !slices.Equal(proxy.Profiles, []string{"browser"}) {
		t.Fatal("browser services are not optional")
	}
	if browser.Image != "${LOKI_BROWSER_IMAGE:-loki-browser:local}" || proxy.Image != "${LOKI_IMAGE:-loki:local}" {
		t.Fatalf("browser images = %q, %q", browser.Image, proxy.Image)
	}
	if !slices.Equal(browser.Networks, []string{"private"}) || !slices.Equal(proxy.Networks, []string{"private", "outbound"}) {
		t.Fatal("browser network boundary is invalid")
	}
	for _, destination := range []string{"/var/lib/loki/browser", "/var/lib/loki/browser-downloads", "/run/loki"} {
		if !hasMount(browser.Volumes, destination) {
			t.Errorf("missing browser mount %s", destination)
		}
	}
	if hasMount(browser.Volumes, "/workspace") || hasMount(browser.Volumes, "/var/lib/loki/runtime") ||
		hasMount(proxy.Volumes, "/workspace") || hasMount(proxy.Volumes, "/var/lib/loki/runtime") {
		t.Fatal("browser profile received project or vault state")
	}
	if len(browser.Ports) != 0 || len(proxy.Ports) != 0 || !hasMount(proxy.Volumes, "/run/loki") {
		t.Fatal("browser profile exposes a host port or lacks the trusted runtime socket")
	}
	if browser.DependsOn["browser-proxy"].Condition != "service_healthy" ||
		proxy.DependsOn["runtime"].Condition != "service_healthy" {
		t.Fatal("browser readiness order is incomplete")
	}
	if !strings.Contains(strings.Join(compose.Services["prepare"].Command, "\n"), "install -d -o 10003 -g 10001 -m 0750 /run/loki/browser") {
		t.Fatal("prepare does not create the browser socket directory")
	}
	for _, name := range []string{"runtime", "mcp"} {
		if _, ok := compose.Services[name].DependsOn["browser"]; ok {
			t.Fatalf("%s depends on optional browser", name)
		}
		if _, ok := compose.Services[name].DependsOn["browser-proxy"]; ok {
			t.Fatalf("%s depends on optional browser proxy", name)
		}
	}
}

func TestComposeGitHubPrivateKeyIsRuntimeEphemeral(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var compose composeFile
	if err = yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	for name, service := range compose.Services {
		hasKey := slices.Contains(secretNames(service.Secrets), "github_app_private_key")
		if hasKey != (name == "runtime") {
			t.Errorf("%s GitHub private-key access = %v", name, hasKey)
		}
	}
	runtime := compose.Services["runtime"]
	if secretTarget(runtime.Secrets, "github_app_private_key") != "/run/loki-private/github-app-private-key" ||
		!slices.Contains(runtime.Tmpfs, "/run/loki-private:uid=0,gid=0,mode=0700") ||
		hasMount(runtime.Volumes, "/run/loki-private") {
		t.Fatal("GitHub private key is not isolated in root-only ephemeral storage")
	}
	dockerfile, err := os.ReadFile(filepath.Join(root, "packaging", "container", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dockerfile), "github_app_private_key") || strings.Contains(string(dockerfile), "BEGIN PRIVATE KEY") {
		t.Fatal("GitHub private key is referenced by the image build")
	}
	lifecycle, err := os.ReadFile(filepath.Join(root, "scripts", "loki-compose-lifecycle.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(lifecycle), "loki-private") || !strings.Contains(string(lifecycle), "for n in runtime-state runner-state") {
		t.Fatal("GitHub private key entered the Compose backup set")
	}
}
