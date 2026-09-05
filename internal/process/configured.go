package process

import (
	"sort"
	"time"

	"loki/internal/config"
	"loki/internal/fault"
	"loki/internal/policy"
)

// StartConfigured launches only administrator-configured argv. The caller's
// request context does not own the lifetime of the returned session.
func (m *Manager) StartConfigured(configuration config.Config, paths *policy.Workspace, name string, check bool) (map[string]any, error) {
	commands, kind := configuration.Processes, "process"
	if check {
		commands, kind = configuration.Checks, "check"
	}
	spec, exists := commands[name]
	if !exists {
		return nil, fault.Error("unknown " + kind + ": " + name)
	}
	if !config.RelativeCWD(spec.CWD) {
		return nil, fault.Error("command cwd must stay inside the workspace")
	}
	cwd, err := paths.ResolveCWD(spec.CWD)
	if err != nil {
		return nil, err
	}
	env := map[string]string{"HOME": "/tmp", "TMPDIR": "/tmp", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8",
		"PATH": "/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM": "1", "GIT_OPTIONAL_LOCKS": "0"}
	for name, value := range spec.Environment {
		env[name] = value
	}
	keys := make([]string, 0, len(env))
	for name := range env {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, name := range keys {
		values = append(values, name+"="+env[name])
	}
	return m.Start(StartSpec{Name: name, Spec: Spec{Argv: spec.Command, CWD: cwd, Env: values,
		Timeout: time.Duration(spec.TimeoutSeconds) * time.Second, MaxOutput: spec.MaxOutputBytes}})
}
