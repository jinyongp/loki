// Package secret owns private runtime documents. Only explicitly selected
// metadata is returned to callers; documents never cross the MCP boundary.
package secret

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"loki/internal/fault"
	"loki/internal/policy"
)

const (
	MaxProfiles    = 128
	MaxSecrets     = 512
	MaxActions     = 64
	MaxProjects    = 256
	MaxWorkflows   = 32
	MaxSecretBytes = 1_048_576
)

var (
	profilePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	secretPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
	idPattern      = regexp.MustCompile(`^[a-f0-9]{32}$`)
	routePattern   = regexp.MustCompile(`^/[A-Za-z0-9_-]+(?:/[A-Za-z0-9_-]+)*$`)
)

// A validated object preserves extension fields and JSON integer precision
// when an existing document is updated. Mutations operate only on named keys.
type document map[string]any

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) ([]any, bool)   { a, ok := v.([]any); return a, ok }
func text(v any) (string, bool)   { s, ok := v.(string); return s, ok }
func stringsArray(v any) ([]string, bool) {
	a, ok := array(v)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(a))
	for _, value := range a {
		s, ok := text(value)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}
func number(v any, boolean bool) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		value, err := n.Int64()
		return value, err == nil
	case int:
		return int64(n), true
	case int64:
		return n, true
	case bool:
		if boolean {
			if n {
				return 1, true
			}
			return 0, true
		}
	}
	return 0, false
}
func get(m map[string]any, key string, fallback any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return fallback
}
func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func unique(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func memberSubset(values []string, available map[string]any) bool {
	for _, v := range values {
		if _, ok := available[v]; !ok {
			return false
		}
	}
	return true
}
func exactKeys(m map[string]any, want ...string) bool {
	if len(m) != len(want) {
		return false
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}
func relative(v any, nul bool) bool {
	s, ok := text(v)
	return ok && s != "" && !strings.HasPrefix(s, "/") && !strings.Contains(s, "\\") && (!nul || !strings.ContainsRune(s, 0)) && !slices.Contains(strings.Split(s, "/"), "..")
}
func ProfileName(name string) error {
	if !profilePattern.MatchString(name) {
		return fault.Error("invalid profile name")
	}
	return nil
}
func ActionName(name string) error {
	if !profilePattern.MatchString(name) {
		return fault.Error("invalid action name")
	}
	return nil
}
func SecretName(name string) error {
	if !secretPattern.MatchString(name) {
		return fault.Error("invalid secret name")
	}
	return nil
}

func decodeObject(data json.RawMessage) (map[string]any, error) {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !utf8.Valid(data) || !json.Valid(data) || decoder.Decode(&value) != nil || value == nil {
		return nil, fault.Error("invalid JSON object")
	}
	return value, nil
}

func decode(data json.RawMessage) (document, error) {
	var doc document
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !utf8.Valid(data) || !json.Valid(data) || decoder.Decode(&doc) != nil || doc == nil {
		return nil, fault.Error("secret store schema is invalid")
	}
	if err := validateDocument(doc); err != nil {
		return nil, err
	}
	return doc, nil
}
func Validate(data json.RawMessage) error { _, err := decode(data); return err }
func validateDocument(doc document) error {
	versionOK := doc["version"] == true
	if n, ok := doc["version"].(json.Number); ok {
		v, err := n.Float64()
		versionOK = err == nil && v == 1
	}
	if n, ok := doc["version"].(int); ok {
		versionOK = n == 1
	}
	if !versionOK {
		return fault.Error("secret store schema is invalid")
	}
	profiles := object(doc["profiles"])
	if profiles == nil || len(profiles) > MaxProfiles {
		return fault.Error("secret profile collection is invalid")
	}
	for _, name := range keys(profiles) {
		if err := ProfileName(name); err != nil {
			return err
		}
		profile := object(profiles[name])
		if profile == nil {
			return fault.Error("secret profile is invalid")
		}
		values := object(profile["secrets"])
		actions := object(profile["actions"])
		if values == nil || len(values) > MaxSecrets {
			return fault.Error("secret collection is invalid")
		}
		if actions == nil || len(actions) > MaxActions {
			return fault.Error("action collection is invalid")
		}
		for _, key := range keys(values) {
			if err := SecretName(key); err != nil {
				return err
			}
			v, ok := text(values[key])
			if !ok || len(v) > MaxSecretBytes {
				return fault.Error("secret value is invalid")
			}
		}
		for _, key := range keys(actions) {
			if err := ActionName(key); err != nil {
				return err
			}
			if err := validateAction(actions[key], values); err != nil {
				return err
			}
		}
	}
	if _, exists := doc["projects"]; !exists {
		doc["projects"] = map[string]any{}
	}
	projects := object(doc["projects"])
	if projects == nil || len(projects) > MaxProjects {
		return fault.Error("project collection is invalid")
	}
	for _, id := range keys(projects) {
		if err := validateProject(id, projects[id], profiles); err != nil {
			return err
		}
	}
	return nil
}

func validateProject(id string, value any, profiles map[string]any) error {
	if !idPattern.MatchString(id) {
		return fault.Error("project id is invalid")
	}
	p := object(value)
	if p == nil || !exactKeys(p, "name", "repository", "workflows") {
		return fault.Error("project policy is invalid")
	}
	name, ok := text(p["name"])
	if !ok || !profilePattern.MatchString(name) {
		return fault.Error("project name is invalid")
	}
	if !relative(p["repository"], false) {
		return fault.Error("project repository is invalid")
	}
	workflows := object(p["workflows"])
	if workflows == nil || len(workflows) > MaxWorkflows {
		return fault.Error("project workflow collection is invalid")
	}
	for _, name := range keys(workflows) {
		if !profilePattern.MatchString(name) {
			return fault.Error("workflow name is invalid")
		}
		if err := validateWorkflow(workflows[name], profiles); err != nil {
			return err
		}
	}
	return nil
}
func validateWorkflow(value any, profiles map[string]any) error {
	w := object(value)
	if w == nil || !exactKeys(w, "steps", "required_secrets", "timeout_seconds") {
		return fault.Error("project workflow policy is invalid")
	}
	steps, ok := array(w["steps"])
	if !ok || len(steps) == 0 || len(steps) > 64 {
		return fault.Error("project workflow steps are invalid")
	}
	pairs := [][]string{}
	for _, step := range steps {
		pair, ok := stringsArray(step)
		if !ok || len(pair) != 2 {
			return fault.Error("project workflow steps are invalid")
		}
		pairs = append(pairs, pair)
	}
	required := object(w["required_secrets"])
	if required == nil {
		return fault.Error("project workflow required secrets are invalid")
	}
	timeout, ok := number(w["timeout_seconds"], false)
	if !ok || timeout < 1 || timeout > 86400 {
		return fault.Error("project workflow timeout is invalid")
	}
	referenced := map[string]bool{}
	for _, pair := range pairs {
		referenced[pair[0]] = true
	}
	for name := range required {
		referenced[name] = true
	}
	names := []string{}
	for name := range referenced {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ProfileName(name); err != nil {
			return err
		}
		if _, exists := profiles[name]; !exists {
			return fault.Error("project workflow references an unknown profile")
		}
	}
	for _, pair := range pairs {
		if err := ActionName(pair[1]); err != nil {
			return err
		}
		if _, exists := object(object(profiles[pair[0]])["actions"])[pair[1]]; !exists {
			return fault.Error("project workflow references an unknown action")
		}
	}
	for _, profile := range keys(required) {
		names, ok := stringsArray(required[profile])
		if !ok || !unique(names) {
			return fault.Error("project workflow required secrets are invalid")
		}
		values := object(object(profiles[profile])["secrets"])
		for _, name := range names {
			if err := SecretName(name); err != nil {
				return err
			}
			if _, exists := values[name]; !exists {
				return fault.Error("project workflow references an unknown secret")
			}
		}
	}
	return nil
}

func validateAction(value any, available map[string]any) error {
	a := object(value)
	if a == nil {
		return fault.Error("action policy is invalid")
	}
	command, ok := stringsArray(a["command"])
	if !ok || len(command) == 0 {
		return fault.Error("action command must be a non-empty argv array")
	}
	length := 0
	for _, arg := range command {
		if arg == "" || strings.ContainsRune(arg, 0) {
			return fault.Error("action command must be a non-empty argv array")
		}
		length += utf8.RuneCountInString(arg)
	}
	if len(command) > 129 || length > 16384 {
		return fault.Error("action command is too large")
	}
	if _, ok := policy.ExecutablePath(command[0]); !ok {
		return fault.Error("action executable is not allowed")
	}
	if err := policy.ValidateExec(command[0], command[1:]); err != nil {
		return err
	}
	if !relative(a["cwd"], true) {
		return fault.Error("action cwd must be workspace-relative")
	}
	selected, ok := stringsArray(a["secrets"])
	if !ok {
		return fault.Error("action secrets must be an array")
	}
	all, ok := get(a, "all_secrets", len(selected) == 0).(bool)
	if !ok {
		return fault.Error("action all_secrets must be a boolean")
	}
	if all && len(selected) > 0 {
		return fault.Error("action cannot combine all_secrets with selected secrets")
	}
	if !all && len(selected) == 0 {
		return fault.Error("action must select secrets or enable all_secrets")
	}
	if !unique(selected) || !memberSubset(selected, available) {
		return fault.Error("action references an unknown or duplicate secret")
	}
	required, ok := stringsArray(get(a, "required_secrets", []any{}))
	if !ok {
		return fault.Error("action required_secrets must be an array")
	}
	if !unique(required) || !memberSubset(required, available) {
		return fault.Error("action references an unknown or duplicate required secret")
	}
	if !all {
		for _, name := range required {
			if !slices.Contains(selected, name) {
				return fault.Error("required secrets must be selected by the action")
			}
		}
	}
	timeout, ok := number(a["timeout_seconds"], true)
	if !ok || timeout < 1 || timeout > 86400 {
		return fault.Error("action timeout is invalid")
	}
	maximum, ok := number(a["max_output_bytes"], true)
	if !ok || maximum < 4096 || maximum > 16777216 {
		return fault.Error("action output limit is invalid")
	}
	if name, exists := a["materialize_env_file"]; exists && name != nil {
		s, ok := text(name)
		if !ok {
			return fault.Error("invalid secret name")
		}
		if err := SecretName(s); err != nil {
			return err
		}
	}
	if path := a["materialize_env_path"]; path != nil {
		if a["materialize_env_file"] == nil {
			return fault.Error("materialized env path requires a materialized env file")
		}
		if !relative(path, true) {
			return fault.Error("materialized env path must be workspace-relative")
		}
	}
	if _, ok := get(a, "docker_access", false).(bool); !ok {
		return fault.Error("action docker access must be a boolean")
	}
	if raw := a["dynamic_port"]; raw != nil {
		d := object(raw)
		if d == nil || !(exactKeys(d, "preferred", "environment") || exactKeys(d, "preferred", "environment", "origin_environment")) {
			return fault.Error("action dynamic port policy is invalid")
		}
		port, ok := number(d["preferred"], false)
		name, stringOK := text(d["environment"])
		if !ok || port < 1024 || port > 65535 || port >= 8765 && port <= 8767 || !stringOK {
			return fault.Error("action dynamic port policy is invalid")
		}
		if err := SecretName(name); err != nil {
			return err
		}
		if origin := d["origin_environment"]; origin != nil {
			name, ok := text(origin)
			if !ok {
				return fault.Error("invalid secret name")
			}
			if err := SecretName(name); err != nil {
				return err
			}
		}
		count := 0
		for _, arg := range command {
			if arg == "{LOKI_PORT}" {
				count++
			}
		}
		if count > 1 {
			return fault.Error("dynamic port action may contain at most one {LOKI_PORT} argument")
		}
	} else if slices.Contains(command, "{LOKI_PORT}") {
		return fault.Error("action port placeholder requires a dynamic port policy")
	}
	callback, ok := get(a, "local_callback", false).(bool)
	if !ok {
		return fault.Error("action local callback policy must be a boolean")
	}
	if callback && a["dynamic_port"] == nil {
		return fault.Error("local callback target requires a dynamic port policy")
	}
	public, ok := stringsArray(get(a, "public_environment", []any{}))
	if !ok || len(public) > 64 || !unique(public) {
		return fault.Error("action public environment policy is invalid")
	}
	for _, name := range public {
		if err := SecretName(name); err != nil {
			return err
		}
	}
	preview := object(get(a, "preview_environment", map[string]any{}))
	if preview == nil || len(preview) > 7 {
		return fault.Error("invalid preview environment policy")
	}
	for _, name := range keys(preview) {
		prefix, ok := text(preview[name])
		if !slices.Contains(public, name) || !ok || !routePattern.MatchString(prefix) {
			return fault.Error("invalid preview environment route")
		}
	}
	if _, ok := get(a, "singleton", false).(bool); !ok {
		return fault.Error("action singleton must be a boolean")
	}
	if probe := a["lock_probe"]; probe != nil {
		path, ok := text(probe)
		if !ok || path == "" {
			return fault.Error("action lock probe must be a relative path")
		}
		if strings.HasPrefix(path, "/") || slices.Contains(strings.Split(path, "/"), "..") {
			return fault.Error("action lock probe must stay inside action cwd")
		}
	}
	return nil
}
