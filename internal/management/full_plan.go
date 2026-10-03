package management

import (
	"fmt"
	"path/filepath"
	"slices"

	"loki/internal/tools"
)

// FullPlan is a credential-free desired deployment. Public selections and
// private service prerequisites are separate; an executor needed by Git does
// not expose the job tool. Installed-but-disabled modules start no services.
type FullPlan struct {
	Schema   int               `json:"schema"`
	Release  string            `json:"release"`
	Enabled  []tools.Selection `json:"enabled"`
	Programs []FullProgram     `json:"programs"`
	Services []FullService     `json:"services"`
}

type FullProgram struct {
	Module     tools.ID `json:"module"`
	Identity   string   `json:"identity"`
	Generation string   `json:"generation"`
}

type FullService struct {
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Requires   []string `json:"requires"`
	Network    string   `json:"network"`
	DataOwners []string `json:"data_owners"`
}

func (s Store) PlanFull() (FullPlan, error) {
	state, err := s.Load()
	if err != nil {
		return FullPlan{}, err
	}
	if state.Config.Mode != tools.Full || LocalTarget(tools.Full).OS != "linux" {
		return FullPlan{}, fmt.Errorf("full deployment requires a Linux host configured for full mode")
	}
	plan := FullPlan{Schema: 1, Release: state.Config.Release, Enabled: []tools.Selection{}, Programs: []FullProgram{}, Services: []FullService{}}
	var manifests []tools.Manifest
	for _, installed := range state.Installed {
		manifests = append(manifests, installed.Manifest)
	}
	registry, err := tools.NewRegistry(manifests)
	if err != nil {
		return plan, err
	}
	var selected []tools.ID
	for _, choice := range state.Config.Tools {
		if choice.Enabled {
			plan.Enabled = append(plan.Enabled, choice)
			selected = append(selected, choice.ID)
		}
	}
	slices.SortFunc(plan.Enabled, func(a, b tools.Selection) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	resolution, err := registry.Resolve(LocalTarget(tools.Full), selected)
	if err != nil {
		return plan, err
	}
	resources := map[tools.ID]bool{}
	for _, module := range resolution.Ordered {
		installation := state.Installed[module.ID]
		generation, err := s.Generation(installation.Artifact)
		if err != nil {
			return plan, err
		}
		if err := verifyOwner(generation, installation.Artifact); err != nil {
			return plan, err
		}
		resources[module.ID] = true
		plan.Programs = append(plan.Programs, FullProgram{Module: module.ID, Identity: installation.Artifact.Identity(), Generation: generation})
	}
	if len(selected) == 0 {
		return plan, nil
	}
	enabled := map[tools.ID]tools.Selection{}
	for _, choice := range plan.Enabled {
		enabled[choice.ID] = choice
	}
	add := func(name, role, network string, requires, owners []string) {
		slices.Sort(requires)
		slices.Sort(owners)
		plan.Services = append(plan.Services, FullService{Name: name, Role: role, Network: network, Requires: requires, DataOwners: owners})
	}
	runtimeNeeded := resources["browser"] || resources["github"] || resources["secrets"] || resources["coordination"] || resources["execution"] || resources["sharing"]
	executorNeeded := resources["execution"] || resources["git"] || resources["sharing"]
	mcpDependencies := []string{}
	if runtimeNeeded {
		owners := []string{"runtime-audit"}
		if _, ok := enabled["secrets"]; ok {
			owners = append(owners, "secrets")
		}
		if _, ok := enabled["github"]; ok {
			owners = append(owners, "providers/github")
		}
		if _, ok := enabled["coordination"]; ok {
			owners = append(owners, "coordination")
		}
		add("runtime", "administrator", "private", nil, owners)
		mcpDependencies = append(mcpDependencies, "runtime")
	}
	if executorNeeded {
		add("launcher", "administrator", "none", nil, []string{"execution/launcher", "execution/toolchains"})
		add("executor", "executor", "none", []string{"launcher"}, []string{"execution/jobs"})
		mcpDependencies = append(mcpDependencies, "executor")
	}
	_, publicExecution := enabled["execution"]
	_, publicSharing := enabled["sharing"]
	if publicExecution || publicSharing {
		add("endpoints", "administrator", "host", []string{"launcher"}, nil)
		if publicSharing {
			mcpDependencies = append(mcpDependencies, "endpoints")
		}
	}
	if resources["execution"] || resources["github"] || resources["coordination"] || resources["git"] {
		add("egress", "proxy", "outbound", nil, []string{"egress-audit"})
	}
	if _, ok := enabled["browser"]; ok {
		dependencies := []string{"runtime"}
		if publicExecution || publicSharing {
			dependencies = append(dependencies, "endpoints")
		}
		add("browser-proxy", "proxy", "outbound", dependencies, nil)
		add("browser", "browser", "private", []string{"browser-proxy"}, []string{"browser"})
		mcpDependencies = append(mcpDependencies, "browser")
	}
	if choice, ok := enabled["git"]; ok && slices.Contains(choice.Capabilities, "signing") {
		add("git-signing", "administrator", "none", nil, []string{"git/signing"})
		mcpDependencies = append(mcpDependencies, "git-signing")
	}
	mcpOwners := []string{"mcp-audit"}
	if _, ok := enabled["sharing"]; ok {
		mcpOwners = append(mcpOwners, "sharing")
	}
	add("mcp", "agent", "private", mcpDependencies, mcpOwners)
	slices.SortFunc(plan.Services, func(a, b FullService) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return plan, nil
}

func (p FullPlan) Program(module tools.ID, relative string) (string, error) {
	if !filepath.IsLocal(relative) {
		return "", fmt.Errorf("program path must stay inside its immutable module generation")
	}
	for _, program := range p.Programs {
		if program.Module == module {
			return filepath.Join(program.Generation, relative), nil
		}
	}
	return "", fmt.Errorf("module %s is absent from the enabled resource closure", module)
}
