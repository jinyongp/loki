package egress

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRepositoryDependencyPolicy(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "go", "egress-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := LoadPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	hosts := policy.Profiles["dependency-install"].AllowedHosts
	for _, host := range []string{
		"registry.npmjs.org",
		"pypi.org",
		"files.pythonhosted.org",
		"proxy.golang.org",
		"sum.golang.org",
		"cdn.playwright.dev",
		"playwright.download.prss.microsoft.com",
		"storage.googleapis.com",
	} {
		if !slices.Contains(hosts, host) {
			t.Fatalf("dependency policy does not include %q", host)
		}
	}
}

func TestPolicyRejectsAmbiguousHostsAndPorts(t *testing.T) {
	for _, profile := range []Profile{
		{AllowedHosts: []string{"registry.npmjs.org"}, AllowedPorts: []int{80}},
		{AllowedHosts: []string{"REGISTRY.npmjs.org"}, AllowedPorts: []int{443}},
		{AllowedHosts: []string{"127.0.0.1"}, AllowedPorts: []int{443}},
		{AllowedHosts: []string{"registry.npmjs.org", "registry.npmjs.org"}, AllowedPorts: []int{443}},
	} {
		policy := Policy{Version: PolicyVersion, Profiles: map[string]Profile{"dependency-install": profile}}
		if err := policy.Validate(); err == nil {
			t.Fatalf("unsafe policy accepted: %#v", profile)
		}
	}
}
