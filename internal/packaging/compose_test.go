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
	Networks map[string]struct {
		Internal bool `yaml:"internal"`
	} `yaml:"networks"`
}

type composeService struct {
	Image       string   `yaml:"image"`
	Command     []string `yaml:"command"`
	User        string   `yaml:"user"`
	Networks    []string `yaml:"networks"`
	Ports       []string `yaml:"ports"`
	Volumes     []string `yaml:"volumes"`
	Secrets     []string `yaml:"secrets"`
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
	for _, name := range []string{"prepare", "egress", "runtime", "mcp"} {
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
	if !slices.Equal(compose.Services["mcp"].Secrets, []string{"mcp_token"}) {
		t.Fatal("MCP token is not a read-only Compose secret")
	}
	if hasMount(compose.Services["mcp"].Volumes, "/var/lib/loki/runtime") || !hasMount(compose.Services["runtime"].Volumes, "/var/lib/loki/runtime") {
		t.Fatal("runtime vault mount is not isolated")
	}
	if compose.Services["runtime"].DependsOn["egress"].Condition != "service_healthy" ||
		compose.Services["mcp"].DependsOn["runtime"].Condition != "service_healthy" {
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
