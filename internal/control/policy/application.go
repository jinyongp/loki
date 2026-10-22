package policy

import (
	"errors"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const MaxApplicationKeys = 256

var (
	ApplicationUUID     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	ApplicationName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	ApplicationAlias    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	ApplicationEnv      = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)
	applicationDNSLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// ApplicationResource is host policy, never project configuration. It contains
// no values and cannot select a platform credential profile.
type ApplicationResource struct {
	ID           string             `json:"id"`
	Alias        string             `json:"alias"`
	Repository   string             `json:"repository"`
	Principals   []ApplicationGrant `json:"principals"`
	Environments []string           `json:"environments"`
	Bundle       string             `json:"bundle,omitempty"`
	Keys         map[string]string  `json:"keys"`
	Network      string             `json:"network"`
	Destinations []string           `json:"destinations"`
}

type ApplicationGrant struct {
	Principal string `json:"principal"`
	Metadata  bool   `json:"metadata"`
	Write     bool   `json:"write"`
	Execute   bool   `json:"execute"`
}

func (r ApplicationResource) Profile() string {
	return "app-" + strings.ReplaceAll(r.ID, "-", "")
}

// ReservedApplicationKey also rejects every name in the effective execution
// base, including future contract additions. Case folding covers proxy aliases.
func ReservedApplicationKey(name string, base map[string]string) bool {
	upper := strings.ToUpper(name)
	for key := range base {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	for _, prefix := range []string{"XDG_", "LD_", "DYLD_", "GIT_CONFIG", "DOCKER_", "LOKI_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	switch upper {
	case "PATH", "HOME", "TMPDIR", "SSH_AUTH_SOCK", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "BASH_ENV", "ENV", "SHELLOPTS", "NODE_OPTIONS", "PYTHONHOME", "PYTHONPATH", "RUBYOPT", "PERL5OPT":
		return true
	}
	return false
}

// CanonicalApplications validates before publication and detaches all mutable
// collections from the caller. Reordering policy does not change its digest.
func CanonicalApplications(resources []ApplicationResource, base map[string]string) ([]ApplicationResource, error) {
	result := make([]ApplicationResource, 0, len(resources))
	ids, aliases := map[string]bool{}, map[string]bool{}
	invalid := errors.New("invalid application resource policy")
	for _, r := range resources {
		if !ApplicationUUID.MatchString(r.ID) || !ApplicationAlias.MatchString(r.Alias) || ids[r.ID] || aliases[r.Alias] || strings.TrimSpace(r.Repository) != r.Repository || r.Repository == "" || len(r.Repository) > 2048 || strings.ContainsAny(r.Repository, "\x00\r\n") || len(r.Principals) == 0 || len(r.Keys) == 0 || len(r.Keys) > MaxApplicationKeys || len(r.Environments) == 0 {
			return nil, invalid
		}
		ids[r.ID], aliases[r.Alias] = true, true
		keys := make(map[string]string, len(r.Keys))
		for name, kind := range r.Keys {
			if !ApplicationName.MatchString(name) || ReservedApplicationKey(name, base) || (kind != "variable" && kind != "secret") {
				return nil, invalid
			}
			keys[name] = kind
		}
		r.Keys = keys
		r.Environments = slices.Clone(r.Environments)
		sort.Strings(r.Environments)
		for i, env := range r.Environments {
			if !ApplicationEnv.MatchString(env) || i > 0 && env == r.Environments[i-1] {
				return nil, invalid
			}
		}
		if r.Bundle != "" && !ApplicationAlias.MatchString(r.Bundle) {
			return nil, invalid
		}
		r.Principals = slices.Clone(r.Principals)
		sort.Slice(r.Principals, func(i, j int) bool { return r.Principals[i].Principal < r.Principals[j].Principal })
		for i, g := range r.Principals {
			if !ApplicationAlias.MatchString(g.Principal) || !g.Metadata && !g.Write && !g.Execute || i > 0 && g.Principal == r.Principals[i-1].Principal {
				return nil, invalid
			}
		}
		r.Destinations = slices.Clone(r.Destinations)
		sort.Strings(r.Destinations)
		if r.Network != "none" && r.Network != "application" || r.Network == "none" && len(r.Destinations) > 0 || r.Network == "application" && len(r.Destinations) == 0 {
			return nil, invalid
		}
		for i, d := range r.Destinations {
			host, port, err := net.SplitHostPort(d)
			if err != nil || port != "443" || host == "" || strings.ToLower(host) != host || strings.ContainsAny(host, "/*@\x00\r\n ") || net.ParseIP(host) != nil || i > 0 && d == r.Destinations[i-1] {
				return nil, invalid
			}
			if len(host) > 253 {
				return nil, invalid
			}
			for _, label := range strings.Split(host, ".") {
				if !applicationDNSLabel.MatchString(label) {
					return nil, invalid
				}
			}
		}
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (r ApplicationResource) Authorize(principal, repository, env, operation string) error {
	if env == "" {
		env = "common"
	}
	if repository != r.Repository || !slices.Contains(r.Environments, env) {
		return errors.New("application resource is not authorized")
	}
	for _, g := range r.Principals {
		if g.Principal == principal && (operation == "metadata" && g.Metadata || operation == "write" && g.Write || operation == "execute" && g.Execute) {
			return nil
		}
	}
	return errors.New("application resource is not authorized")
}
