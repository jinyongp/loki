package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"loki/internal/config"
	"loki/internal/fault"
	"loki/internal/project"
)

type DynamicPort struct {
	Preferred         int    `json:"preferred"`
	Environment       string `json:"environment"`
	OriginEnvironment string `json:"origin_environment"`
}

type ActionPolicy struct {
	Command            []string          `json:"command"`
	CWD                string            `json:"cwd"`
	Secrets            []string          `json:"secrets"`
	RequiredSecrets    []string          `json:"required_secrets"`
	AllSecrets         bool              `json:"all_secrets"`
	TimeoutSeconds     int               `json:"timeout_seconds"`
	MaxOutputBytes     int               `json:"max_output_bytes"`
	MaterializeEnvFile string            `json:"materialize_env_file"`
	MaterializeEnvPath string            `json:"materialize_env_path"`
	DockerAccess       bool              `json:"docker_access"`
	DynamicPort        *DynamicPort      `json:"dynamic_port"`
	LocalCallback      bool              `json:"local_callback"`
	PublicEnvironment  []string          `json:"public_environment"`
	PreviewEnvironment map[string]string `json:"preview_environment"`
	Singleton          bool              `json:"singleton"`
	LockProbe          string            `json:"lock_probe"`
}

// ActionPlan is a private runtime-only snapshot. It is never an RPC result.
// Explicit credential accessors are used only by the privileged launch path;
// diagnostics and JSON serialization cannot reveal its private fields.
type ActionPlan struct {
	Profile, Action, CWD, VisibleCWD string
	Policy                           ActionPolicy
	values                           map[string]string
	previewValues                    map[string]string
}

func (p ActionPlan) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private action plan]")
}
func (p ActionPlan) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private action plan cannot be serialized")
}

func (p ActionPlan) SecretEnvironment() []string {
	out := make([]string, 0, len(p.values))
	for _, name := range p.SelectedNames() {
		out = append(out, name+"="+p.values[name])
	}
	return out
}

func (p ActionPlan) RedactionValues() []string {
	out := make([]string, 0, len(p.values))
	for _, name := range p.SelectedNames() {
		if value := p.values[name]; value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (p ActionPlan) SelectedNames() []string {
	if !p.Policy.AllSecrets {
		return slices.Clone(p.Policy.Secrets)
	}
	names := make([]string, 0, len(p.values))
	for name := range p.values {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (c Controller) ResolveAction(ctx context.Context, profileName, actionName string, override *string) (ActionPlan, error) {
	return c.resolveAction(ctx, profileName, actionName, override, false)
}

// ResolveMaterialization reads only the registered cleanup policy. Clearing a
// stale mount target does not require configured credentials or expose values.
func (c Controller) ResolveMaterialization(ctx context.Context, profileName, actionName string) (ActionPlan, error) {
	return c.resolveAction(ctx, profileName, actionName, nil, true)
}

func (c Controller) resolveAction(ctx context.Context, profileName, actionName string, override *string, cleanup bool) (ActionPlan, error) {
	var plan ActionPlan
	if err := ProfileName(profileName); err != nil {
		return plan, err
	}
	if err := ActionName(actionName); err != nil {
		return plan, err
	}
	doc, err := c.load(ctx)
	if err != nil {
		return plan, err
	}
	p, err := profile(doc, profileName)
	if err != nil {
		return plan, err
	}
	a := object(object(p["actions"])[actionName])
	if a == nil {
		return plan, fault.Error("unknown action")
	}
	values := object(p["secrets"])
	required, _ := stringsArray(get(a, "required_secrets", []any{}))
	missing := []string{}
	for _, name := range required {
		if values[name] == nil || values[name] == "" {
			missing = append(missing, name)
		}
	}
	if !cleanup && len(missing) > 0 {
		slices.Sort(missing)
		return plan, fault.Error("required secrets are not configured: " + strings.Join(missing, ", "))
	}
	selected, _ := stringsArray(a["secrets"])
	normalized := maps.Clone(a)
	normalized["all_secrets"] = get(a, "all_secrets", len(selected) == 0)
	normalized["timeout_seconds"], _ = number(a["timeout_seconds"], true)
	normalized["max_output_bytes"], _ = number(a["max_output_bytes"], true)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return plan, err
	}
	if err = json.Unmarshal(encoded, &plan.Policy); err != nil {
		return ActionPlan{}, errors.New("invalid validated action policy")
	}
	if cleanup && (plan.Policy.MaterializeEnvFile == "" || plan.Policy.MaterializeEnvPath == "") {
		return ActionPlan{}, fault.Error("action has no fixed secret materialization target")
	}
	plan.CWD, err = actionCWD(ctx, c.Projects, plan.Policy.CWD, override)
	if err != nil {
		return ActionPlan{}, err
	}
	relative, _ := filepath.Rel(c.Projects.WorkspaceRoot, plan.CWD)
	plan.VisibleCWD = filepath.ToSlash(filepath.Join("/workspace", relative))
	plan.Profile, plan.Action = profileName, actionName
	if cleanup {
		return plan, nil
	}
	if plan.Policy.AllSecrets {
		selected = keys(values)
	}
	plan.values = make(map[string]string, len(selected))
	for _, name := range selected {
		plan.values[name] = values[name].(string)
	}
	plan.previewValues = make(map[string]string, len(plan.Policy.PublicEnvironment))
	for _, name := range plan.Policy.PublicEnvironment {
		if value, ok := values[name].(string); ok {
			plan.previewValues[name] = value
		}
	}
	return plan, nil
}

func inside(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func actionCWD(ctx context.Context, projects *project.Store, registered string, override *string) (string, error) {
	if projects == nil {
		return "", errors.New("action workspace is not configured")
	}
	resolve := func(relative string) (string, error) {
		target, err := filepath.EvalSymlinks(filepath.Join(projects.WorkspaceRoot, relative))
		if err != nil {
			return "", fault.Error("action cwd is not a directory")
		}
		if !inside(projects.WorkspaceRoot, target) {
			return "", fault.Error("action cwd escapes workspace")
		}
		info, err := os.Stat(target)
		if err != nil || !info.IsDir() {
			return "", fault.Error("action cwd is not a directory")
		}
		return target, nil
	}
	base, err := resolve(registered)
	if err != nil || override == nil {
		return base, err
	}
	if *override == "" || !config.RelativeCWD(*override) {
		return "", fault.Error("action cwd override must be workspace-relative")
	}
	candidate, err := resolve(*override)
	if err != nil || candidate == base {
		return candidate, err
	}
	identity := func(cwd string) (project.Identity, error) {
		id, err := projects.Resolve(ctx, cwd)
		if err != nil {
			if strings.Contains(err.Error(), "Git metadata escapes workspace") {
				return id, fault.Error("action Git metadata must stay inside the workspace")
			}
			if _, public := err.(fault.Error); public {
				return id, fault.Error("action cwd override requires a Git worktree")
			}
			return id, fault.Error("unable to verify action worktree")
		}
		return id, nil
	}
	a, err := identity(candidate)
	if err != nil {
		return "", err
	}
	b, err := identity(base)
	if err != nil {
		return "", err
	}
	if a.LogicalCommonDirectory != b.LogicalCommonDirectory {
		return "", fault.Error("action cwd override must be a worktree of the registered repository")
	}
	return candidate, nil
}

type PreviewBindings struct {
	BackendRoutes       map[string]int    `json:"backend_routes"`
	EnvironmentRoutes   map[string]string `json:"environment_routes"`
	EnvironmentSuffixes map[string]string `json:"environment_suffixes"`
	RequiredEnvironment []string          `json:"required_environment"`
}

func (p ActionPlan) PreviewBindings() (PreviewBindings, error) {
	result := PreviewBindings{map[string]int{}, map[string]string{}, map[string]string{}, []string{}}
	for _, name := range p.Policy.PublicEnvironment {
		value := p.previewValues[name]
		parsed, err := url.Parse(value)
		if err != nil {
			return result, fault.Error("invalid preview backend URL")
		}
		if !slices.Contains([]string{"127.0.0.1", "localhost", "::1", "0.0.0.0"}, strings.ToLower(parsed.Hostname())) {
			continue
		}
		result.RequiredEnvironment = append(result.RequiredEnvironment, name)
		prefix, configured := p.Policy.PreviewEnvironment[name]
		if !configured {
			continue
		}
		hasCredentials := false
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			hasCredentials = parsed.User.Username() != "" || password != ""
		}
		if parsed.Scheme != "http" || hasCredentials || parsed.RawQuery != "" || parsed.Fragment != "" {
			return result, fault.Error("invalid preview backend URL")
		}
		port := 80
		if raw := parsed.Port(); raw != "" {
			port, err = strconv.Atoi(raw)
			if err != nil || port < 0 || port > 65535 {
				return result, fault.Error("invalid preview backend URL")
			}
			if port == 0 {
				port = 80
			}
		}
		if previous, exists := result.BackendRoutes[prefix]; exists && previous != port {
			return result, fault.Error("preview route has conflicting backends")
		}
		result.BackendRoutes[prefix] = port
		result.EnvironmentRoutes[name] = prefix
		result.EnvironmentSuffixes[name] = strings.TrimRight(parsed.EscapedPath(), "/")
	}
	if p.Policy.DynamicPort != nil && p.Policy.DynamicPort.OriginEnvironment != "" {
		origin := p.Policy.DynamicPort.OriginEnvironment
		result.EnvironmentRoutes[origin] = "/"
		result.RequiredEnvironment = append(result.RequiredEnvironment, origin)
	}
	return result, nil
}

func (p ActionPlan) PublicEnvironment(values map[string]string, baseDomain string, requireMapping bool) (map[string]string, error) {
	allowed := slices.Clone(p.Policy.PublicEnvironment)
	if p.Policy.DynamicPort != nil && p.Policy.DynamicPort.OriginEnvironment != "" {
		allowed = append(allowed, p.Policy.DynamicPort.OriginEnvironment)
	}
	if len(values) > 0 && (!config.Hostname(baseDomain) || !strings.Contains(baseDomain, ".")) {
		return nil, fault.Error("preview domain is not configured")
	}
	pattern := regexp.MustCompile(`^https://loki-[0-9a-f]{32}\.` + regexp.QuoteMeta(baseDomain) + `(?:/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*)?$`)
	result := make(map[string]string, len(values))
	for name, value := range values {
		if !slices.Contains(allowed, name) || !pattern.MatchString(value) {
			return nil, fault.Error("public preview environment is invalid")
		}
		result[name] = value
	}
	if requireMapping {
		bindings, err := p.PreviewBindings()
		if err != nil {
			return nil, err
		}
		for _, name := range bindings.RequiredEnvironment {
			if _, exists := result[name]; !exists {
				return nil, fault.Error("PREVIEW_MAPPING_REQUIRED: missing public dependency or origin mapping")
			}
		}
	}
	return result, nil
}
