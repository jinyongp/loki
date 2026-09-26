// Package policy compiles validated administrator-owned inputs into the
// immutable control-plane policy generation consumed by Loki roles.
package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"loki/internal/config"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/execution"
)

const DocumentVersion = 1

type Document struct {
	Version       int       `json:"version"`
	WorkspaceRoot string    `json:"workspace_root"`
	AuditLog      string    `json:"audit_log"`
	Listener      Listener  `json:"listener"`
	Sharing       Sharing   `json:"sharing"`
	Access        Access    `json:"access"`
	Limits        Limits    `json:"limits"`
	Execution     Execution `json:"execution"`
	GitHub        GitHub    `json:"github"`
}

type Listener struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	PublicHosts []string `json:"public_hosts"`
}

type Sharing struct {
	ArtifactBaseURL   string `json:"artifact_base_url"`
	PreviewBaseDomain string `json:"preview_base_domain"`
}

// Access preserves schema-v1 provider configuration in the effective-policy
// digest for rollback/config identity only. New providers must not extend this
// compatibility payload. Remove it only with an explicit config-schema migration
// that drops v1 read/rollback compatibility; provider-specific verification
// remains outside the policy/compiler.
type Access struct {
	CloudflareTeamDomain  string `json:"cloudflare_team_domain"`
	CloudflareAudience    string `json:"cloudflare_audience"`
	PreviewAccessAudience string `json:"preview_access_audience"`
}

type Limits struct {
	MaxFileBytes                int `json:"max_file_bytes"`
	MaxWriteBytes               int `json:"max_write_bytes"`
	MaxOutputBytes              int `json:"max_output_bytes"`
	MaxListEntries              int `json:"max_list_entries"`
	MaxSearchResults            int `json:"max_search_results"`
	MaxReadLines                int `json:"max_read_lines"`
	MaxPatchBytes               int `json:"max_patch_bytes"`
	MaxPatchFiles               int `json:"max_patch_files"`
	GitHubMaxResponseBytes      int `json:"github_max_response_bytes"`
	GitHubMaxPages              int `json:"github_max_pages"`
	GitHubCommandTimeoutSeconds int `json:"github_command_timeout_seconds"`
	GitHubMaxInputBytes         int `json:"github_max_input_bytes"`
	GitHubMaxOutputBytes        int `json:"github_max_output_bytes"`
}

type Execution struct {
	Version         int              `json:"version"`
	Directories     []Directory      `json:"directories"`
	Environment     []Variable       `json:"environment"`
	NetworkProfiles []NetworkProfile `json:"network_profiles"`
}

type Directory struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Owner string `json:"owner"`
	Group string `json:"group"`
	Mode  string `json:"mode"`
}

type Variable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type NetworkProfile struct {
	Name                   string `json:"name"`
	Mode                   string `json:"mode"`
	Proxy                  string `json:"proxy,omitempty"`
	AllowSecrets           bool   `json:"allow_secrets"`
	AdministratorAllowlist bool   `json:"administrator_allowlist"`
}

type GitHub struct {
	AppID      int64          `json:"app_id"`
	APIVersion string         `json:"api_version"`
	Targets    []GitHubTarget `json:"targets"`
}

type GitHubTarget struct {
	Target         string `json:"target"`
	AccountType    string `json:"account_type"`
	InstallationID int64  `json:"installation_id"`
}

func Compile(c config.Config, contract execution.Contract) (controlpolicy.Generation, error) {
	document, err := compileDocument(c, contract)
	if err != nil {
		return controlpolicy.Generation{}, err
	}
	return controlpolicy.NewGeneration(document)
}

func CompileFiles(configPath, githubConfigPath, executionContractPath string) (controlpolicy.Generation, execution.Contract, error) {
	c, err := config.LoadWithGitHub(configPath, githubConfigPath)
	if err != nil {
		return controlpolicy.Generation{}, execution.Contract{}, err
	}
	raw, err := os.ReadFile(executionContractPath)
	if err != nil {
		return controlpolicy.Generation{}, execution.Contract{}, err
	}
	contract, err := execution.Load(raw)
	if err != nil {
		return controlpolicy.Generation{}, execution.Contract{}, err
	}
	generation, err := Compile(c, contract)
	if err != nil {
		return controlpolicy.Generation{}, execution.Contract{}, err
	}
	return generation, contract, nil
}

func compileDocument(c config.Config, contract execution.Contract) (Document, error) {
	if err := contract.Validate(); err != nil {
		return Document{}, err
	}
	if !cleanAbsolute(c.Root) || !cleanAbsolute(c.AuditLog) {
		return Document{}, errors.New("effective policy requires clean absolute workspace and audit paths")
	}
	workspace, ok := contract.Directories["workspace"]
	if !ok || c.Root != workspace.Path {
		return Document{}, errors.New("configured workspace root does not match execution contract")
	}

	publicHosts := append([]string(nil), c.PublicHosts...)
	sort.Strings(publicHosts)

	directories := make([]Directory, 0, len(contract.Directories))
	for name, directory := range contract.Directories {
		directories = append(directories, Directory{
			Name: name, Path: directory.Path, Owner: directory.Owner,
			Group: directory.Group, Mode: directory.Mode,
		})
	}
	sort.Slice(directories, func(i, j int) bool { return directories[i].Name < directories[j].Name })

	environment := make([]Variable, 0, len(contract.Environment))
	for name, value := range contract.Environment {
		environment = append(environment, Variable{Name: name, Value: value})
	}
	sort.Slice(environment, func(i, j int) bool { return environment[i].Name < environment[j].Name })

	networks := make([]NetworkProfile, 0, len(contract.NetworkProfiles))
	for name, profile := range contract.NetworkProfiles {
		networks = append(networks, NetworkProfile{
			Name: name, Mode: profile.Mode, Proxy: profile.Proxy,
			AllowSecrets:           profile.AllowSecrets,
			AdministratorAllowlist: profile.AdministratorAllowlist,
		})
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })

	targets, err := githubTargets(c)
	if err != nil {
		return Document{}, err
	}

	return Document{
		Version:       DocumentVersion,
		WorkspaceRoot: c.Root,
		AuditLog:      c.AuditLog,
		Listener:      Listener{Host: c.Host, Port: c.Port, PublicHosts: publicHosts},
		Sharing:       Sharing{ArtifactBaseURL: c.ArtifactBaseURL, PreviewBaseDomain: c.PreviewBaseDomain},
		Access: Access{
			CloudflareTeamDomain:  c.CloudflareTeamDomain,
			CloudflareAudience:    c.CloudflareAudience,
			PreviewAccessAudience: c.PreviewAccessAudience,
		},
		Limits: Limits{
			MaxFileBytes: c.MaxFileBytes, MaxWriteBytes: c.MaxWriteBytes,
			MaxOutputBytes: c.MaxOutputBytes, MaxListEntries: c.MaxListEntries,
			MaxSearchResults: c.MaxSearchResults, MaxReadLines: c.MaxReadLines,
			MaxPatchBytes: c.MaxPatchBytes, MaxPatchFiles: c.MaxPatchFiles,
			GitHubMaxResponseBytes:      c.GitHubMaxResponseBytes,
			GitHubMaxPages:              c.GitHubMaxPages,
			GitHubCommandTimeoutSeconds: c.GitHubCommandTimeoutSeconds,
			GitHubMaxInputBytes:         c.GitHubMaxInputBytes,
			GitHubMaxOutputBytes:        c.GitHubMaxOutputBytes,
		},
		Execution: Execution{
			Version: contract.Version, Directories: directories,
			Environment: environment, NetworkProfiles: networks,
		},
		GitHub: GitHub{AppID: c.GitHubAppID, APIVersion: c.GitHubAPIVersion, Targets: targets},
	}, nil
}

func githubTargets(c config.Config) ([]GitHubTarget, error) {
	if c.GitHubAppID == 0 {
		if len(c.GitHubInstallations) != 0 || len(c.GitHubTargets) != 0 {
			return nil, errors.New("GitHub policy is inconsistent with disabled GitHub App")
		}
		return []GitHubTarget{}, nil
	}
	if len(c.GitHubInstallations) == 0 {
		return nil, errors.New("GitHub policy requires installations")
	}
	targets := make([]GitHubTarget, 0, len(c.GitHubTargets))
	seen := make(map[string]struct{}, len(c.GitHubTargets))
	for _, installation := range c.GitHubInstallations {
		for _, repository := range installation.Repositories {
			target := installation.Account + "/" + repository
			if _, exists := seen[target]; exists {
				return nil, fmt.Errorf("duplicate GitHub policy target %q", target)
			}
			seen[target] = struct{}{}
			targets = append(targets, GitHubTarget{
				Target: target, AccountType: installation.AccountType,
				InstallationID: installation.InstallationID,
			})
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Target == targets[j].Target {
			return targets[i].InstallationID < targets[j].InstallationID
		}
		return targets[i].Target < targets[j].Target
	})
	configured := append([]string(nil), c.GitHubTargets...)
	sort.Strings(configured)
	if len(configured) != len(targets) {
		return nil, errors.New("GitHub policy targets do not match installations")
	}
	for index, target := range targets {
		if configured[index] != target.Target {
			return nil, errors.New("GitHub policy targets do not match installations")
		}
	}
	return targets, nil
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}
