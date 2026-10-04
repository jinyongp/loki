package management

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"loki/internal/tools"
)

// FullMount records the finite authority granted to one service. Persistent
// data are separate named volumes per owner; sockets are separate per peer.
// Only immutable active module generations and the credential-free activation
// directory use host binds. The backend never mounts the management root.
type FullMount struct {
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type FullServiceResources struct {
	HostNetwork         bool        `json:"host_network,omitempty"`
	Name                string      `json:"name"`
	Image               string      `json:"image"`
	UID                 uint32      `json:"uid"`
	GID                 uint32      `json:"gid"`
	SupplementaryGroups []uint32    `json:"supplementary_groups"`
	Mounts              []FullMount `json:"mounts"`
	Networks            []string    `json:"networks"`
}

type FullTopology struct {
	Schema    int                    `json:"schema"`
	Owner     string                 `json:"owner"`
	Resources FullResources          `json:"resources"`
	Services  []FullServiceResources `json:"services"`
}

func (s Store) FullOwner() string {
	return fmt.Sprintf("loki-tools-%x", sha256.Sum256([]byte(filepath.Clean(s.Root))))[:27]
}

var fullDataTargets = map[string]string{
	"workspace":            "/workspace",
	"execution/runner":     "/var/lib/loki/runner",
	"execution/cache":      "/var/cache/loki",
	"runtime-audit":        "/var/lib/loki/runtime",
	"secrets":              "/var/lib/loki/secrets",
	"providers/github":     "/var/lib/loki/providers/github",
	"coordination":         "/var/lib/loki/coordination",
	"execution/launcher":   "/var/lib/loki/launcher",
	"execution/toolchains": "/var/lib/loki/toolchains",
	"execution/jobs":       "/var/lib/loki/executor",
	"egress-audit":         "/var/log/loki",
	"browser":              "/var/lib/loki/browser",
	"git/signing":          "/var/lib/loki/git/signing",
	"mcp-audit":            "/var/lib/loki/mcp",
	"sharing":              "/var/lib/loki/sharing",
}

func fullModuleMount(program FullProgram) FullMount {
	return FullMount{Kind: "bind", Source: program.Generation, Target: "/opt/loki/modules/" + string(program.Module), ReadOnly: true}
}

func (s Store) FullTopology() (FullTopology, error) {
	resources, err := s.FullResources()
	if err != nil {
		return FullTopology{}, err
	}
	result := FullTopology{Schema: 1, Owner: s.FullOwner(), Resources: resources, Services: []FullServiceResources{}}
	if len(resources.Plan.Services) == 0 {
		return result, nil
	}
	coreImage, err := resources.Image("runtime-core", "service")
	if err != nil {
		return result, err
	}
	active := map[tools.ID]bool{}
	for _, program := range resources.Plan.Programs {
		active[program.Module] = true
	}
	public := map[tools.ID]bool{}
	for _, choice := range resources.Plan.Enabled {
		public[choice.ID] = true
	}
	data := func(owner string, readOnly bool) (FullMount, error) {
		target, ok := fullDataTargets[owner]
		if !ok {
			return FullMount{}, fmt.Errorf("unknown service data owner %s", owner)
		}
		return FullMount{Kind: "volume", Source: result.Owner + "-data-" + strings.ReplaceAll(owner, "/", "-"), Target: target, ReadOnly: readOnly}, nil
	}
	socket := func(peer string, readOnly bool) FullMount {
		return FullMount{Kind: "volume", Source: result.Owner + "-socket-" + peer, Target: "/run/loki/" + peer, ReadOnly: readOnly}
	}
	for _, service := range resources.Plan.Services {
		entry := FullServiceResources{Name: service.Name, Image: coreImage, UID: 10000, GID: 10000, SupplementaryGroups: []uint32{10001}, Mounts: []FullMount{}, Networks: []string{}}
		switch service.Role {
		case "administrator":
			entry.UID, entry.GID = 0, 0
		case "executor":
			entry.UID, entry.GID = 10004, 10004
		case "proxy":
			entry.UID, entry.GID = 10002, 10002
			if service.Name == "browser-proxy" {
				entry.UID, entry.GID = 10005, 10005
			}
		case "browser":
			entry.UID, entry.GID = 10003, 10003
			entry.Image, err = resources.Image("browser", "browser")
			if err != nil {
				return result, err
			}
		case "agent":
		default:
			return result, fmt.Errorf("unknown full role")
		}
		if service.Network == "host" {
			if service.Name != "endpoints" {
				return result, fmt.Errorf("only the owned endpoint relay uses host networking")
			}
			entry.HostNetwork = true
		} else if service.Network != "none" {
			network := "private"
			if service.Name == "browser" || service.Name == "browser-proxy" {
				network = "browser-private"
			}
			entry.Networks = append(entry.Networks, result.Owner+"-"+network)
			if service.Network == "outbound" {
				entry.Networks = append(entry.Networks, result.Owner+"-outbound")
			}
		}
		for _, owner := range service.DataOwners {
			mount, err := data(owner, false)
			if err != nil {
				return result, err
			}
			entry.Mounts = append(entry.Mounts, mount)
		}
		// Every service uses the protected worker; vendor resources belong to
		// only their consumers. The browser receives no project workspace,
		// Git programs, provider or application-secret state.
		needed := map[tools.ID]bool{"runtime-core": true}
		switch service.Name {
		case "mcp":
			if public["workspace"] || public["git"] || public["sharing"] || public["coordination"] {
				mount, _ := data("workspace", false)
				entry.Mounts = append(entry.Mounts, mount)
			}
			for _, choice := range resources.Plan.Enabled {
				if choice.ID == "workspace" || choice.ID == "git" || choice.ID == "sharing" || choice.ID == "execution" {
					needed[choice.ID] = true
				}
			}
			for _, peer := range service.Requires {
				if peer == "git-signing" {
					peer = "signing"
				}
				entry.Mounts = append(entry.Mounts, socket(peer, true))
			}
		case "runtime":
			if active["execution"] || public["github"] || public["coordination"] || public["sharing"] {
				for _, owner := range []string{"workspace", "execution/runner", "execution/cache"} {
					mount, _ := data(owner, false)
					entry.Mounts = append(entry.Mounts, mount)
				}
			}
			for _, choice := range resources.Plan.Enabled {
				if choice.ID == "github" || choice.ID == "coordination" {
					needed[choice.ID] = true
				}
			}
			entry.Mounts = append(entry.Mounts, socket("runtime", false))
		case "browser":
			needed["browser"] = true
			entry.Mounts = append(entry.Mounts, socket("browser", false))
		case "browser-proxy":
			entry.Mounts = append(entry.Mounts, socket("runtime", true))
			for _, dependency := range service.Requires {
				if dependency == "endpoints" {
					entry.Mounts = append(entry.Mounts, socket("endpoints", true))
				}
			}
		case "endpoints":
			entry.Mounts = append(entry.Mounts, socket("endpoints", false), socket("launcher", true))
		case "launcher":
			needed["execution"] = true
			mount, _ := data("workspace", false)
			entry.Mounts = append(entry.Mounts, mount)
			entry.Mounts = append(entry.Mounts, socket("launcher", false))
		case "executor":
			needed["execution"] = true
			entry.Mounts = append(entry.Mounts, socket("executor", false), socket("launcher", true))
		case "git-signing":
			needed["git"] = true
			entry.Image, err = resources.Image("git", "git-workload")
			if err != nil {
				return result, err
			}
			entry.Mounts = append(entry.Mounts, socket("signing", false))
		}
		for _, program := range resources.Plan.Programs {
			if needed[program.Module] {
				entry.Mounts = append(entry.Mounts, fullModuleMount(program))
			}
		}
		if service.Name == "runtime" || service.Name == "mcp" || service.Name == "git-signing" || service.Name == "endpoints" {
			entry.Mounts = append(entry.Mounts, FullMount{Kind: "bind", Source: s.ControlDirectory(), Target: "/etc/loki/activation", ReadOnly: true})
		}
		if service.Name == "mcp" {
			for _, choice := range resources.Plan.Enabled {
				if choice.ID == "execution" {
					mount, _ := data("execution/toolchains", true)
					entry.Mounts = append(entry.Mounts, mount)
				}
			}
		}
		slices.SortFunc(entry.Mounts, func(a, b FullMount) int { return strings.Compare(a.Target, b.Target) })
		for i := 1; i < len(entry.Mounts); i++ {
			if entry.Mounts[i-1].Target == entry.Mounts[i].Target {
				return result, fmt.Errorf("service mount collision")
			}
		}
		result.Services = append(result.Services, entry)
	}
	return result, nil
}
