// Package config reads the existing Loki TOML configuration.
package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var audiencePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hostLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

type Config struct {
	Root, AuditLog, Host                                         string
	Port                                                         int
	PublicHosts                                                  []string
	CloudflareTeamDomain, CloudflareAudience                     string
	ArtifactBaseURL, PreviewBaseDomain, PreviewAccessAudience    string
	MaxFileBytes, MaxWriteBytes, MaxOutputBytes, MaxListEntries  int
	MaxSearchResults, MaxReadLines, MaxPatchBytes, MaxPatchFiles int
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
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
	return c, nil
}

func bounded(raw map[string]any, key string, def, min, max int) (int, error) {
	v, ok := raw[key]
	if !ok {
		return def, nil
	}
	var n int
	var err error
	switch value := v.(type) {
	case int64:
		n = int(value)
	case string:
		n, err = strconv.Atoi(strings.TrimSpace(value))
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value > float64(max) || value < float64(min) {
			err = errors.New("range")
		} else {
			n = int(value)
		}
	default:
		err = errors.New("integer")
	}
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d", key, min, max)
	}
	return n, nil
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
