package management

import (
	"slices"
	"strings"
	"testing"

	"loki/internal/tools"
)

func layoutResources(selected []tools.Selection) FullResources {
	result := FullResources{Plan: FullPlan{Release: Release, Enabled: selected, Services: []FullService{{Name: "runtime"}, {Name: "mcp"}}, Programs: []FullProgram{{Module: "runtime-core"}}}, Payloads: map[tools.ID]FullPayload{}}
	for _, id := range []tools.ID{"runtime-core", "git", "execution", "github"} {
		result.Payloads[id] = FullPayload{Programs: map[string]string{}, Assets: map[string]string{}, Images: map[string]string{}}
	}
	result.Payloads["runtime-core"].Assets["execution-contract"] = "config/contract.json"
	result.Payloads["runtime-core"].Assets["egress-policy"] = "config/egress.json"
	result.Payloads["runtime-core"].Programs["loki"] = "bin/loki"
	result.Payloads["runtime-core"].Images["gateway"] = "example.com/gateway@sha256:" + strings.Repeat("a", 64)
	result.Payloads["execution"].Images["workload"] = "example.com/workload@sha256:" + strings.Repeat("b", 64)
	result.Payloads["git"].Images["git-workload"] = "example.com/git@sha256:" + strings.Repeat("c", 64)
	result.Payloads["git"].Programs["git"] = "native/bin/git"
	result.Payloads["git"].Assets["templates"] = "templates"
	result.Payloads["git"].Assets["gitconfig"] = "config/gitconfig"
	result.Payloads["github"].Programs["gh"] = "native/bin/gh"
	return result
}

func TestLayoutsKeepPrivateGitExecutionOutOfPublicTools(t *testing.T) {
	resources := layoutResources([]tools.Selection{{ID: "git", Enabled: true}})
	resources.Plan.Services = append(resources.Plan.Services, FullService{Name: "executor"}, FullService{Name: "launcher"}, FullService{Name: "egress"})
	resources.Plan.Programs = append(resources.Plan.Programs, FullProgram{Module: "execution"}, FullProgram{Module: "git"})
	layout, err := resources.Layouts()
	if err != nil {
		t.Fatal(err)
	}
	if got := layout.MCP["Tools"].([]string); !slices.Equal(got, []string{"git"}) {
		t.Fatalf("private executor became public: %v", got)
	}
	if !slices.Contains(layout.Runtime["Tools"].([]string), "execution") {
		t.Fatal("Git lost private execution operations")
	}
	if _, ok := layout.MCP["ToolchainCatalog"]; ok {
		t.Fatal("private execution acquired public job toolchain configuration")
	}
	if _, ok := layout.Runtime["GitHubStateDirectory"]; ok {
		t.Fatal("Git received provider credentials")
	}
	if layout.MCP["GitBinary"] != "/opt/loki/modules/git/native/bin/git" {
		t.Fatal("Git used ambient executable")
	}
}

func TestLayoutsPersonalAuthorizationIsExplicitAndSecretsHaveSeparateOwner(t *testing.T) {
	resources := layoutResources([]tools.Selection{{ID: "github", Enabled: true}, {ID: "secrets", Enabled: true}})
	resources.Plan.Programs = append(resources.Plan.Programs, FullProgram{Module: "github"}, FullProgram{Module: "secrets"})
	layout, err := resources.Layouts()
	if err != nil {
		t.Fatal(err)
	}
	if layout.Runtime["PersonalProjects"] != false {
		t.Fatal("default setup requested optional user authorization")
	}
	if layout.Runtime["GitHubStateDirectory"] == layout.Runtime["SecretStateDirectory"] {
		t.Fatal("provider and application credentials share data owner")
	}
	resources.Plan.Enabled[0].Capabilities = []string{"personal-projects"}
	layout, err = resources.Layouts()
	if err != nil || layout.Runtime["PersonalProjects"] != true {
		t.Fatalf("explicit personal authorization missing: %v", err)
	}
}
