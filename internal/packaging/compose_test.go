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
	DependsOn   map[string]struct {
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
}

func hasMount(mounts []string, destination string) bool {
	for _, mount := range mounts {
		if strings.HasSuffix(mount, ":"+destination) || mount == destination {
			return true
		}
	}
	return false
}
