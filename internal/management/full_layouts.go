package management

import (
	"fmt"
	"slices"

	"loki/internal/tools"
)

// FullLayouts are private worker inputs produced from the exact active module
// resources. They separate public MCP selections from private runtime workers.
// The backend publishes them as protected read-only files before starting roles.
type FullLayouts struct {
	MCP               map[string]any `json:"mcp"`
	Runtime           map[string]any `json:"runtime,omitempty"`
	Launcher          map[string]any `json:"launcher,omitempty"`
	Executor          map[string]any `json:"executor,omitempty"`
	ExecutionContract string         `json:"execution_contract"`
	EgressPolicy      string         `json:"egress_policy,omitempty"`
}

func (r FullResources) Layouts() (FullLayouts, error) {
	result := FullLayouts{MCP: map[string]any{}}
	if len(r.Plan.Services) == 0 {
		return result, nil
	}
	contract, err := r.ServiceAsset("runtime-core", "execution-contract")
	if err != nil {
		return result, err
	}
	result.ExecutionContract = contract
	services := map[string]bool{}
	for _, service := range r.Plan.Services {
		services[service.Name] = true
	}
	public := []string{}
	selected := map[tools.ID]tools.Selection{}
	for _, choice := range r.Plan.Enabled {
		public = append(public, string(choice.ID))
		selected[choice.ID] = choice
	}
	gate := map[string]any{"Path": "/etc/loki/activation/state.json", "Release": r.Plan.Release, "Mode": "full", "Snapshot": true}
	result.MCP = map[string]any{
		"Tools": public, "ToolsConfigPath": "/etc/loki/activation/state.json", "ToolsConfigSnapshot": true,
		"ExecutionContract": contract, "Environment": map[string]string{"HOME": "/home/runner", "PATH": "/opt/loki/bin:/usr/local/bin:/usr/bin:/bin", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"},
	}
	if _, ok := selected["workspace"]; ok {
		program, err := r.ServiceProgram("workspace", "rg")
		if err != nil {
			return result, err
		}
		root, err := r.ServiceAsset("workspace", "skills")
		if err != nil {
			return result, err
		}
		result.MCP["RGPath"], result.MCP["PackagedSkillRoot"] = program, root
	}
	if _, ok := selected["git"]; ok {
		program, err := r.ServiceProgram("git", "git")
		if err != nil {
			return result, err
		}
		root, err := r.ServiceAsset("git", "templates")
		if err != nil {
			return result, err
		}
		result.MCP["GitBinary"], result.MCP["GitTemplateRoots"] = program, []string{root}
		configuration, err := r.ServiceAsset("git", "gitconfig")
		if err != nil {
			return result, err
		}
		result.MCP["Environment"].(map[string]string)["GIT_CONFIG_GLOBAL"] = configuration
	}
	if services["runtime"] {
		// Browser has its own private service and only contributes port lookup
		// to the runtime. Private execution is admitted for Git without job MCP.
		runtimeTools := []string{}
		for _, program := range r.Plan.Programs {
			if program.Module == "execution" || program.Module == "browser" || program.Module == "github" || program.Module == "secrets" || program.Module == "coordination" || program.Module == "sharing" {
				runtimeTools = append(runtimeTools, string(program.Module))
			}
		}
		result.Runtime = map[string]any{
			"Tools": runtimeTools, "ToolGate": gate, "Socket": "/run/loki/runtime/control.sock", "SocketGID": 10001, "AgentUID": 10000, "RunnerUID": 10000, "RunnerGID": 10000,
			"StateDirectory": "/var/lib/loki/runtime", "AuditPath": "/var/lib/loki/runtime/audit.jsonl", "ExecutionContract": contract,
			"Workspace": "/workspace", "SnapshotDirectory": "/var/lib/loki/runner/snapshots",
		}
		result.MCP["RuntimeSocket"], result.MCP["RuntimeUID"] = "/run/loki/runtime/control.sock", 0
		if slices.Contains(runtimeTools, "browser") {
			result.Runtime["PortInspectorUID"] = 10005
			if !slices.Contains(runtimeTools, "execution") && !slices.Contains(runtimeTools, "github") && !slices.Contains(runtimeTools, "coordination") && !slices.Contains(runtimeTools, "sharing") {
				result.Runtime["Workspace"] = "/var/lib/loki/runtime/ports"
			}
		}
		if _, ok := selected["secrets"]; ok {
			result.Runtime["SecretStateDirectory"], result.Runtime["InboxDirectory"] = "/var/lib/loki/secrets", "/var/lib/loki/secrets/inbox"
		}
		if choice, ok := selected["github"]; ok {
			program, err := r.ServiceProgram("github", "gh")
			if err != nil {
				return result, err
			}
			result.Runtime["GitHubBinary"], result.Runtime["GitHubStateDirectory"], result.Runtime["GitHubTempDirectory"] = program, "/var/lib/loki/providers/github", "/var/tmp/loki/github"
			result.Runtime["GitHubProxy"] = "http://egress:18766"
			result.Runtime["PersonalProjects"] = slices.Contains(choice.Capabilities, "personal-projects")
		}
		if _, ok := selected["coordination"]; ok {
			program, err := r.ServiceProgram("coordination", "devtools")
			if err != nil {
				return result, err
			}
			result.Runtime["DevtoolsBinary"], result.Runtime["CoordinationStateDirectory"] = program, "/var/lib/loki/coordination"
		}
		if slices.Contains(runtimeTools, "execution") || slices.Contains(runtimeTools, "sharing") {
			result.Runtime["DockerSocket"] = "/run/docker.sock"
		}
	}
	if choice, ok := selected["browser"]; ok {
		result.MCP["BrowserProtocol"], result.MCP["BrowserCapabilities"] = "official", choice.Capabilities
		result.MCP["BrowserSocket"], result.MCP["BrowserUID"] = "/run/loki/browser/control.sock", 10003
	}
	if services["executor"] {
		result.MCP["ExecutorSocket"], result.MCP["ExecutorUID"] = "/run/loki/executor/control.sock", 10004
		result.Executor = map[string]any{"Socket": "/run/loki/executor/control.sock", "SocketGID": 10001, "AgentUID": 10000, "ExecutorUID": 10004, "LauncherSocket": "/run/loki/launcher/control.sock", "LauncherUID": 0, "RunTimeoutSeconds": 86400}
		image, err := r.Image("execution", "workload")
		if err != nil {
			return result, err
		}
		if _, ok := selected["git"]; ok {
			image, err = r.Image("git", "git-workload")
			if err != nil {
				return result, err
			}
		}
		gateway, err := r.Image("runtime-core", "gateway")
		if err != nil {
			return result, err
		}
		binary, err := r.ServiceProgram("runtime-core", "loki")
		if err != nil {
			return result, err
		}
		egress, err := r.ServiceAsset("runtime-core", "egress-policy")
		if err != nil {
			return result, err
		}
		result.EgressPolicy = egress
		result.Launcher = map[string]any{
			"Socket": "/run/loki/launcher/control.sock", "SocketGID": 10001, "ExecutorUID": 10004, "StateDirectory": "/var/lib/loki/launcher", "DockerSocket": "/run/docker.sock", "DockerPeerUID": 0,
			"Image": image, "GatewayImage": gateway, "GatewayBinary": binary, "GatewayExecutionContract": contract, "GatewayEgressPolicy": egress, "GatewayProxyPort": 18766, "GatewayMemoryBytes": 134217728, "GatewayPIDs": 64, "GatewayTmpfsBytes": 33554432,
			"Workspace": "/workspace", "ToolchainStore": "/var/lib/loki/toolchains", "WorkloadUID": 10000, "WorkloadGID": 10000, "MemoryBytes": 1073741824, "PIDs": 256, "TmpfsBytes": 67108864,
			"RunTimeoutSeconds": 86400, "ResultRetentionSeconds": 3600, "MaxJobs": 64, "MaxConcurrentJobs": 8, "MaxOutputBytes": 262144,
		}
	}
	if services["egress"] && result.EgressPolicy == "" {
		result.EgressPolicy, err = r.ServiceAsset("runtime-core", "egress-policy")
		if err != nil {
			return result, err
		}
	}
	if _, ok := selected["execution"]; ok {
		catalog, err := r.ServiceAsset("execution", "toolchain-catalog")
		if err != nil {
			return result, err
		}
		result.MCP["ToolchainStore"], result.MCP["ToolchainCatalog"] = "/var/lib/loki/toolchains", catalog
		result.MCP["PortGuardSocket"], result.MCP["PortGuardUID"] = "/run/loki/runtime/control.sock", 0
	}
	if _, ok := selected["sharing"]; ok {
		result.MCP["EndpointSocket"] = "/run/loki/endpoints/control.sock"
		result.MCP["PortGuardSocket"], result.MCP["PortGuardUID"] = "/run/loki/runtime/control.sock", 0
	}
	if len(public) == 0 {
		return result, fmt.Errorf("nonempty full service layout requires a public tool")
	}
	return result, nil
}
