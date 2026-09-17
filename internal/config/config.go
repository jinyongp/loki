// Package config reads the existing Loki TOML configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var audiencePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hostLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
var githubAccountPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
var githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

type GitHubInstallation struct {
	Account        string
	AccountType    string
	InstallationID int64
	Repositories   []string
}

type Config struct {
	Root, AuditLog, Host                                         string
	Port                                                         int
	PublicHosts                                                  []string
	CloudflareTeamDomain, CloudflareAudience                     string
	ArtifactBaseURL, PreviewBaseDomain, PreviewAccessAudience    string
	MaxFileBytes, MaxWriteBytes, MaxOutputBytes, MaxListEntries  int
	MaxSearchResults, MaxReadLines, MaxPatchBytes, MaxPatchFiles int
	GitHubAppID                                                  int64
	GitHubAPIVersion                                             string
	GitHubInstallations                                          []GitHubInstallation
	GitHubTargets                                                []string
	GitHubMaxResponseBytes, GitHubMaxPages                       int
	GitHubCommandTimeoutSeconds                                  int
	GitHubMaxInputBytes, GitHubMaxOutputBytes                    int
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
}

// LoadWithGitHub merges a deployment-provided public GitHub TOML fragment
// into the primary configuration. The fragment is limited to GitHub settings
// so a mounted Compose config cannot alter runtime isolation.
func LoadWithGitHub(path, githubPath string) (Config, error) {
	if githubPath == "" {
		return Load(path)
	}
	base, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	fragment, err := os.ReadFile(githubPath)
	if err != nil {
		return Config{}, err
	}
	var primary, injected map[string]any
	if toml.Unmarshal(base, &primary) != nil || toml.Unmarshal(fragment, &injected) != nil {
		return Config{}, errors.New("invalid Loki TOML configuration")
	}
	allowed := map[string]bool{
		"github_app_id": true, "github_installations": true, "github_api_version": true,
		"github_max_response_bytes": true, "github_max_pages": true,
		"github_command_timeout_seconds": true, "github_max_input_bytes": true,
		"github_max_output_bytes": true,
	}
	for key, value := range injected {
		if !allowed[key] {
			return Config{}, fmt.Errorf("GitHub configuration contains unsupported setting %q", key)
		}
		if _, exists := primary[key]; exists {
			return Config{}, fmt.Errorf("GitHub setting %q is configured more than once", key)
		}
		primary[key] = value
	}
	merged, err := toml.Marshal(primary)
	if err != nil {
		return Config{}, errors.New("invalid Loki TOML configuration")
	}
	return Parse(merged)
}

func Hostname(host string) bool {
	if len(host) < 1 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func Parse(data []byte) (Config, error) {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return Config{}, errors.New("invalid Loki TOML configuration")
	}
	for key := range raw {
		if _, ok := allowedKeys[key]; !ok {
			return Config{}, fmt.Errorf("unsupported Loki configuration setting %q", key)
		}
	}
	var c Config
	for _, f := range []struct {
		key, def string
		target   *string
	}{
		{"root", "/workspace", &c.Root}, {"audit_log", "/var/log/loki/mcp/audit.jsonl", &c.AuditLog}, {"host", "127.0.0.1", &c.Host},
		{"cloudflare_access_team_domain", "", &c.CloudflareTeamDomain}, {"cloudflare_access_audience", "", &c.CloudflareAudience},
		{"artifact_base_url", "", &c.ArtifactBaseURL}, {"preview_base_domain", "", &c.PreviewBaseDomain}, {"preview_access_audience", "", &c.PreviewAccessAudience},
	} {
		value, ok := raw[f.key]
		if !ok {
			*f.target = f.def
			continue
		}
		s, ok := value.(string)
		if !ok {
			return c, fmt.Errorf("%s must be a string", f.key)
		}
		*f.target = s
	}
	if c.Host != "127.0.0.1" && c.Host != "::1" && c.Host != "0.0.0.0" {
		return c, errors.New("host must be a loopback or container wildcard address")
	}
	for _, f := range []struct {
		key           string
		def, min, max int
		target        *int
	}{
		{"port", 8765, 1024, 65535, &c.Port}, {"max_file_bytes", 16777216, 4096, 268435456, &c.MaxFileBytes}, {"max_write_bytes", 2097152, 4096, 67108864, &c.MaxWriteBytes},
		{"max_output_bytes", 262144, 4096, 16777216, &c.MaxOutputBytes}, {"max_list_entries", 2000, 10, 100000, &c.MaxListEntries}, {"max_search_results", 500, 1, 10000, &c.MaxSearchResults},
		{"max_read_lines", 2000, 1, 20000, &c.MaxReadLines}, {"max_patch_bytes", 524288, 1024, 16777216, &c.MaxPatchBytes}, {"max_patch_files", 50, 1, 1000, &c.MaxPatchFiles},
	} {
		value, err := bounded(raw, f.key, f.def, f.min, f.max)
		if err != nil {
			return c, err
		}
		*f.target = value
	}
	c.PublicHosts = []string{}
	if v, ok := raw["public_hosts"]; ok {
		hosts, err := stringList(v)
		if err != nil {
			return c, errors.New("public_hosts must be an array of hostnames")
		}
		for _, host := range hosts {
			host = strings.ToLower(strings.TrimSpace(host))
			if !Hostname(host) {
				return c, errors.New("invalid public hostname")
			}
			if !slices.Contains(c.PublicHosts, host) {
				c.PublicHosts = append(c.PublicHosts, host)
			}
		}
	}
	c.CloudflareTeamDomain = strings.ToLower(strings.TrimSpace(c.CloudflareTeamDomain))
	c.CloudflareAudience = strings.ToLower(strings.TrimSpace(c.CloudflareAudience))
	_, teamSet := raw["cloudflare_access_team_domain"]
	_, audSet := raw["cloudflare_access_audience"]
	if teamSet != audSet {
		return c, errors.New("Cloudflare Access team domain and audience must be configured together")
	}
	if teamSet {
		if !Hostname(c.CloudflareTeamDomain) || !strings.HasSuffix(c.CloudflareTeamDomain, ".cloudflareaccess.com") {
			return c, errors.New("invalid Cloudflare Access team domain")
		}
		if !audiencePattern.MatchString(c.CloudflareAudience) {
			return c, errors.New("invalid Cloudflare Access audience")
		}
	}
	if _, ok := raw["artifact_base_url"]; ok {
		u, err := url.Parse(strings.TrimSpace(c.ArtifactBaseURL))
		if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || strings.HasSuffix(u.Host, ":") || !slices.Contains(c.PublicHosts, strings.ToLower(u.Hostname())) || strings.TrimRight(u.EscapedPath(), "/") != "/artifacts" {
			return c, errors.New("artifact_base_url must use a configured public host and /artifacts path")
		}
		c.ArtifactBaseURL = "https://" + strings.ToLower(u.Hostname()) + "/artifacts"
	}
	_, previewSet := raw["preview_base_domain"]
	_, previewAudSet := raw["preview_access_audience"]
	if previewAudSet && !previewSet {
		return c, errors.New("preview base domain must be configured before its Access audience")
	}
	if previewSet {
		c.PreviewBaseDomain = strings.ToLower(strings.TrimRight(strings.TrimSpace(c.PreviewBaseDomain), "."))
		if !Hostname(c.PreviewBaseDomain) || !strings.Contains(c.PreviewBaseDomain, ".") {
			return c, errors.New("invalid preview base domain")
		}
	}
	if previewAudSet {
		c.PreviewAccessAudience = strings.ToLower(strings.TrimSpace(c.PreviewAccessAudience))
		if !audiencePattern.MatchString(c.PreviewAccessAudience) {
			return c, errors.New("invalid preview Cloudflare Access audience")
		}
		if !teamSet {
			return c, errors.New("preview Access verification requires Cloudflare Access team settings")
		}
	}
	if err := parseGitHub(raw, &c); err != nil {
		return c, err
	}
	return c, nil
}

func parseGitHub(raw map[string]any, c *Config) error {
	app, appSet, err := positiveInt64(raw, "github_app_id")
	if err != nil {
		return err
	}
	installationValue, installationsSet := raw["github_installations"]
	if appSet != installationsSet {
		return errors.New("GitHub App ID and installations must be configured together")
	}
	c.GitHubAPIVersion = "2026-03-10"
	if value, ok := raw["github_api_version"]; ok {
		version, valid := value.(string)
		if !valid || version != "2026-03-10" {
			return errors.New("unsupported GitHub API version")
		}
		c.GitHubAPIVersion = version
	}
	c.GitHubMaxResponseBytes, err = bounded(raw, "github_max_response_bytes", 1048576, 4096, 16777216)
	if err != nil {
		return err
	}
	c.GitHubMaxPages, err = bounded(raw, "github_max_pages", 20, 1, 100)
	if err != nil {
		return err
	}
	c.GitHubCommandTimeoutSeconds, err = bounded(raw, "github_command_timeout_seconds", 120, 1, 600)
	if err != nil {
		return err
	}
	c.GitHubMaxInputBytes, err = bounded(raw, "github_max_input_bytes", 1048576, 4096, 16777216)
	if err != nil {
		return err
	}
	c.GitHubMaxOutputBytes, err = bounded(raw, "github_max_output_bytes", 4194304, 4096, 16777216)
	if err != nil {
		return err
	}
	if !appSet {
		for _, key := range []string{"github_api_version", "github_max_response_bytes", "github_max_pages", "github_command_timeout_seconds", "github_max_input_bytes", "github_max_output_bytes"} {
			if _, ok := raw[key]; ok {
				return errors.New("GitHub limits require GitHub App configuration")
			}
		}
		return nil
	}
	installations, err := githubInstallationList(installationValue)
	if err != nil || len(installations) == 0 || len(installations) > 16 {
		return errors.New("github_installations must contain 1 to 16 installations")
	}
	installationIDs := []int64{}
	for _, rawInstallation := range installations {
		accountValue, accountSet := rawInstallation["account"]
		accountTypeValue, accountTypeSet := rawInstallation["account_type"]
		id, idSet, idErr := positiveInt64(rawInstallation, "installation_id")
		repositoriesValue, repositoriesSet := rawInstallation["repositories"]
		if idErr != nil {
			return idErr
		}
		account, accountOK := accountValue.(string)
		accountType, accountTypeOK := accountTypeValue.(string)
		if !accountSet || !accountTypeSet || !idSet || !repositoriesSet || !accountOK || !accountTypeOK || len(rawInstallation) != 4 {
			return errors.New("GitHub installation requires account, account_type, installation_id, and repositories")
		}
		account = strings.ToLower(strings.TrimSpace(account))
		accountType = strings.ToLower(strings.TrimSpace(accountType))
		if !githubAccountPattern.MatchString(account) || !slices.Contains([]string{"organization", "user"}, accountType) {
			return errors.New("invalid GitHub installation account")
		}
		if slices.Contains(installationIDs, id) {
			return errors.New("duplicate GitHub installation ID")
		}
		installationIDs = append(installationIDs, id)
		repositories, listErr := stringList(repositoriesValue)
		if listErr != nil || len(repositories) == 0 {
			return errors.New("GitHub installation repositories must not be empty")
		}
		installation := GitHubInstallation{Account: account, AccountType: accountType, InstallationID: id}
		for _, repository := range repositories {
			repository = strings.ToLower(strings.TrimSpace(repository))
			if !githubRepositoryPattern.MatchString(repository) || strings.Contains(repository, "..") {
				return errors.New("invalid GitHub repository name")
			}
			target := account + "/" + repository
			if slices.Contains(c.GitHubTargets, target) {
				return errors.New("duplicate GitHub repository target")
			}
			installation.Repositories = append(installation.Repositories, repository)
			c.GitHubTargets = append(c.GitHubTargets, target)
		}
		c.GitHubInstallations = append(c.GitHubInstallations, installation)
		if len(c.GitHubTargets) > 64 {
			return errors.New("GitHub installations must contain at most 64 repositories")
		}
	}
	c.GitHubAppID = app
	return nil
}

func githubInstallationList(value any) ([]map[string]any, error) {
	switch list := value.(type) {
	case []map[string]any:
		return list, nil
	case []any:
		out := make([]map[string]any, len(list))
		for index, item := range list {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("expected GitHub installation array")
			}
			out[index] = entry
		}
		return out, nil
	default:
		return nil, errors.New("expected GitHub installation array")
	}
}

func positiveInt64(raw map[string]any, key string) (int64, bool, error) {
	value, ok := raw[key]
	if !ok {
		return 0, false, nil
	}
	n, valid := value.(int64)
	if !valid || n <= 0 {
		return 0, true, fmt.Errorf("%s must be a positive integer", key)
	}
	return n, true, nil
}

func bounded(raw map[string]any, key string, def, min, max int) (int, error) {
	v, ok := raw[key]
	if !ok {
		return def, nil
	}
	n, ok := v.(int64)
	if !ok || n < int64(min) || n > int64(max) {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, min, max)
	}
	return int(n), nil
}

func stringList(value any) ([]string, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, errors.New("expected string array")
	}
	out := make([]string, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("expected string array")
		}
		out[i] = s
	}
	return out, nil
}
