// Package admin translates the installed administrator CLI to runtime requests.
package admin

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"loki/internal/fault"
)

type option struct {
	key, kind string
	fallback  any
}
type command struct {
	operation string
	arguments []string
	options   map[string]option
}

var commands = map[string]command{
	"bootstrap":               {"bootstrap_project", []string{"cwd", "workflow?"}, nil},
	"state status":            {"project_state", []string{"cwd"}, nil},
	"project register":        {"project_register", []string{"cwd"}, map[string]option{"name": {"name", "string", nil}}},
	"project unregister":      {"project_unregister", []string{"cwd"}, nil},
	"project status":          {"project_status", []string{"cwd"}, nil},
	"project workflow remove": {"project_remove_workflow", []string{"cwd", "workflow"}, nil},
	"project workflow set": {"project_set_workflow", []string{"cwd", "workflow"}, map[string]option{
		"step": {"steps", "array", []string{}}, "require": {"required_secrets", "array", []string{}}, "timeout-seconds": {"timeout_seconds", "integer", 3600},
	}},
	"secret init":                  {"init", nil, nil},
	"secret status":                {"status", nil, nil},
	"secret list":                  {"list_profiles", nil, nil},
	"secret profile create":        {"profile_create", []string{"profile"}, nil},
	"secret profile remove":        {"profile_remove", []string{"profile"}, nil},
	"secret profile show":          {"get_profile", []string{"profile"}, nil},
	"secret remove":                {"secret_remove", []string{"profile", "secret"}, nil},
	"secret generate":              {"secret_generate", []string{"profile", "secret"}, map[string]option{"bytes": {"bytes", "integer", 32}}},
	"secret audit":                 {"audit", nil, map[string]option{"limit": {"limit", "integer", 50}}},
	"action list":                  {"get_profile", []string{"profile"}, nil},
	"action remove":                {"action_remove", []string{"profile", "action_name"}, nil},
	"action clear-materialization": {"clear_action_materialization", []string{"profile", "action_name"}, nil},
	"action run":                   {"run_action", []string{"profile", "action_name"}, map[string]option{"cwd": {"cwd", "string", nil}, "bind-local-callback": {"bind_local_callback", "bool", false}}},
	"action read":                  {"read_process", []string{"session_id"}, map[string]option{"offset": {"offset", "integer", nil}, "limit": {"limit", "integer", 65536}}},
	"action stop":                  {"stop_process", []string{"session_id"}, nil},
	"action processes":             {"list_processes", nil, nil},
	"action set": {"action_set", []string{"profile", "action_name", "command..."}, map[string]option{
		"cwd": {"cwd", "string", nil}, "all-secrets": {"all_secrets", "bool", false}, "secret": {"secrets", "array", []string{}}, "require-secret": {"required_secrets", "array", []string{}},
		"timeout-seconds": {"timeout_seconds", "integer", 3600}, "max-output-bytes": {"max_output_bytes", "integer", 1048576},
		"materialize-env-file": {"materialize_env_file", "string", nil}, "materialize-env-path": {"materialize_env_path", "string", nil},
		"docker": {"docker_access", "bool", false}, "preferred-port": {"preferred", "integer", nil}, "port-env": {"environment", "string", nil}, "origin-env": {"origin_environment", "string", nil},
		"singleton": {"singleton", "bool", false}, "lock-probe": {"lock_probe", "string", nil}, "local-callback": {"local_callback", "bool", false}, "public-env": {"public_environment", "array", []string{}},
	}},
}

// Request keeps options interspersed with positionals, as in the Python CLI.
// '--' terminates option parsing and preserves child argv byte-for-byte.
func Request(args []string) (map[string]any, error) {
	var spec command
	name := ""
	for n := min(3, len(args)); n > 0; n-- {
		candidate := strings.Join(args[:n], " ")
		if found, ok := commands[candidate]; ok {
			name = candidate
			spec = found
			args = args[n:]
			break
		}
	}
	if name == "" {
		return nil, fault.Error("unknown administration command")
	}
	values := map[string]any{"operation": spec.operation}
	for _, opt := range spec.options {
		values[opt.key] = opt.fallback
	}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		flag, value, inline := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		opt, ok := spec.options[flag]
		if !ok || !strings.HasPrefix(arg, "--") {
			return nil, fault.Error("unknown option: " + flag)
		}
		if opt.kind == "bool" {
			if inline {
				return nil, fault.Error("boolean option takes no value: " + flag)
			}
			values[opt.key] = true
			continue
		}
		if !inline {
			i++
			if i >= len(args) {
				return nil, fault.Error("missing option value: " + flag)
			}
			value = args[i]
		}
		switch opt.kind {
		case "integer":
			n, err := strconv.Atoi(value)
			if err != nil {
				return nil, fault.Error("option must be an integer: " + flag)
			}
			values[opt.key] = n
		case "array":
			values[opt.key] = append(slices.Clone(values[opt.key].([]string)), value)
		default:
			values[opt.key] = value
		}
	}
	for _, key := range spec.arguments {
		if strings.HasSuffix(key, "...") {
			if len(positionals) == 0 {
				return nil, fault.Error("action command is required after --")
			}
			values[strings.TrimSuffix(key, "...")] = positionals
			positionals = nil
			break
		}
		optional := strings.HasSuffix(key, "?")
		key = strings.TrimSuffix(key, "?")
		if len(positionals) == 0 {
			if optional {
				values[key] = "development"
				continue
			}
			return nil, fault.Error("missing argument: " + key)
		}
		values[key] = positionals[0]
		positionals = positionals[1:]
	}
	if len(positionals) > 0 {
		return nil, fault.Error("unexpected positional arguments")
	}
	switch name {
	case "state status":
		values["action"] = "status"
	case "project workflow set":
		steps := [][2]string{}
		for _, text := range values["steps"].([]string) {
			pair, err := parsePair(text)
			if err != nil {
				return nil, err
			}
			steps = append(steps, pair)
		}
		if len(steps) == 0 {
			return nil, fault.Error("at least one --step is required")
		}
		required := map[string][]string{}
		for _, text := range values["required_secrets"].([]string) {
			pair, err := parsePair(text)
			if err != nil {
				return nil, err
			}
			required[pair[0]] = append(required[pair[0]], pair[1])
		}
		values["steps"] = steps
		values["required_secrets"] = required
	case "action set":
		if values["cwd"] == nil {
			return nil, fault.Error("--cwd is required")
		}
		if values["all_secrets"].(bool) == (len(values["secrets"].([]string)) > 0) {
			return nil, fault.Error("select --all-secrets or one or more --secret options")
		}
		if (values["preferred"] == nil) != (values["environment"] == nil) {
			return nil, fault.Error("--preferred-port and --port-env must be used together")
		}
		var dynamic any
		if values["preferred"] != nil {
			port := map[string]any{"preferred": values["preferred"], "environment": values["environment"]}
			if values["origin_environment"] != nil {
				port["origin_environment"] = values["origin_environment"]
			}
			dynamic = port
		} else if values["origin_environment"] != nil {
			return nil, fault.Error("--origin-env requires a dynamic port")
		}
		values["dynamic_port"] = dynamic
		delete(values, "preferred")
		delete(values, "environment")
		delete(values, "origin_environment")
		request := map[string]any{"operation": values["operation"], "profile": values["profile"], "action_name": values["action_name"]}
		delete(values, "operation")
		delete(values, "profile")
		delete(values, "action_name")
		request["action"] = values
		return request, nil
	}
	return values, nil
}

func parsePair(value string) ([2]string, error) {
	profile, name, ok := strings.Cut(value, "/")
	if !ok || profile == "" || name == "" {
		return [2]string{}, fault.Error(fmt.Sprintf("%q must use PROFILE/NAME", value))
	}
	return [2]string{profile, name}, nil
}
