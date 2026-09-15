// Package admin translates the installed administrator CLI to runtime requests.
package admin

import (
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
	"secret init":           {"init", nil, nil},
	"secret status":         {"status", nil, nil},
	"secret list":           {"list_profiles", nil, nil},
	"secret profile create": {"profile_create", []string{"profile"}, nil},
	"secret profile remove": {"profile_remove", []string{"profile"}, nil},
	"secret profile show":   {"get_profile", []string{"profile"}, nil},
	"secret remove":         {"secret_remove", []string{"profile", "secret"}, nil},
	"secret generate":       {"secret_generate", []string{"profile", "secret"}, map[string]option{"bytes": {"bytes", "integer", 32}}},
	"secret audit":          {"audit", nil, map[string]option{"limit": {"limit", "integer", 50}}},
	"secret set":            {"secret_set", []string{"profile", "secret"}, nil},
	"secret import-env":     {"import_env", []string{"profile", "file"}, map[string]option{"delete-source": {"delete_source", "bool", false}}},
	"secret stage-env":      {"stage_env", []string{"file"}, map[string]option{"delete-source": {"delete_source", "bool", false}}},
	"github app-key set":    {"github_app_key_set", nil, nil},
	"github fields list":    {"github_fields_list", []string{"target"}, nil},
	"github values list":    {"github_values_list", []string{"target"}, map[string]option{"issue": {"issue", "integer", 0}}},
	"github values add":     {"github_values_add", []string{"target", "values_json"}, map[string]option{"issue": {"issue", "integer", 0}}},
	"github values set":     {"github_values_set", []string{"target", "values_json"}, map[string]option{"issue": {"issue", "integer", 0}}},
	"github values clear":   {"github_values_clear", []string{"target"}, map[string]option{"issue": {"issue", "integer", 0}, "field-id": {"field_id", "integer", 0}}},
}

// Request keeps options interspersed with positional arguments.
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
	return values, nil
}
