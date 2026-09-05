// Package commands executes configured checks and policy-approved workspace tools.
// Its host process must run inside the installed MCP filesystem/network sandbox.
package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"loki/internal/config"
	"loki/internal/fault"
	"loki/internal/gitops"
	"loki/internal/policy"
	"loki/internal/process"
)

type Controller struct {
	Config  config.Config
	Paths   *policy.Workspace
	Manager *process.Manager
	Git     *gitops.Controller
	// Environment overrides are private service configuration, never tool input.
	Environment map[string]string
}
type Request struct {
	Action           string
	Arguments        []string
	CWD              string
	NodeVersion      *string `json:"node_version"`
	Timeout          int     `json:"timeout_seconds"`
	Name, Executable *string
}

var nodePattern = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`)

func (c *Controller) NodeVersion(cwd string, requested *string) (string, error) {
	if requested != nil {
		if *requested == "" || utf8.RuneCountInString(*requested) > 128 || strings.ContainsAny(*requested, "\x00/\\") {
			return "", fault.Error("invalid Node version")
		}
		return *requested, nil
	}
	rel, err := filepath.Rel(c.Paths.Root(), cwd)
	if err != nil {
		return "", err
	}
	for _, name := range []string{".node-version", ".nvmrc"} {
		file, err := c.Paths.Open(filepath.Join(rel, name), os.O_RDONLY, 0)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		file.Close()
		if err == nil && info.Mode().IsRegular() {
			return filepath.Join(cwd, name), nil
		}
	}
	file, err := c.Paths.Open(".loki/fnm/node-versions", os.O_RDONLY, 0)
	if err != nil {
		return "", fault.Error("no FNM-managed Node version is installed")
	}
	defer file.Close()
	entries, err := file.ReadDir(4097)
	if err != nil && len(entries) == 0 {
		return "", fault.Error("no FNM-managed Node version is installed")
	}
	if len(entries) > 4096 {
		return "", fault.Error("too many installed Node versions")
	}
	best := ""
	var maximum [3]uint64
	for _, entry := range entries {
		match := nodePattern.FindStringSubmatch(entry.Name())
		if match == nil || !entry.IsDir() {
			continue
		}
		var version [3]uint64
		valid := true
		for index := range 3 {
			number, err := strconv.ParseUint(match[index+1], 10, 64)
			if err != nil {
				valid = false
				break
			}
			version[index] = number
		}
		if !valid {
			continue
		}
		if best == "" || version[0] > maximum[0] || version[0] == maximum[0] && (version[1] > maximum[1] || version[1] == maximum[1] && version[2] > maximum[2]) {
			maximum = version
			best = entry.Name()
		}
	}
	if best == "" {
		return "", fault.Error("no FNM-managed Node version is installed")
	}
	return best, nil
}
func arguments(args []string, empty bool) error {
	if !empty && len(args) == 0 {
		return fault.Error("command arguments cannot be empty")
	}
	if len(args) > 64 {
		return fault.Error("too many command arguments")
	}
	for _, arg := range args {
		if arg == "" || strings.ContainsRune(arg, 0) || utf8.RuneCountInString(arg) > 4096 {
			return fault.Error("invalid command argument")
		}
	}
	return nil
}
func (c *Controller) environment(tool bool, extra map[string]string) []string {
	values := map[string]string{}
	for _, entry := range process.Environment() {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	if tool {
		for name, value := range map[string]string{
			"FNM_DIR": "/workspace/.loki/fnm", "FNM_NODE_DIST_MIRROR": "https://nodejs.org/dist", "FNM_VERSION_FILE_STRATEGY": "recursive", "COREPACK_HOME": "/workspace/.loki/corepack", "COREPACK_ENABLE_DOWNLOAD_PROMPT": "0",
			"GOCACHE": "/workspace/.loki/go/build-cache", "GOMODCACHE": "/workspace/.loki/go/pkg/mod", "GOPATH": "/workspace/.loki/go", "GOPROXY": "https://proxy.golang.org",
			"CARGO_HOME": "/workspace/.loki/cargo", "RUSTUP_HOME": "/workspace/.loki/rustup", "CARGO_REGISTRIES_CRATES_IO_PROTOCOL": "sparse", "CARGO_NET_GIT_FETCH_WITH_CLI": "true", "CI": "1", "NPM_CONFIG_REGISTRY": "https://registry.npmjs.org", "npm_config_store_dir": "/workspace/.loki/pnpm-store",
			"HTTPS_PROXY": "http://127.0.0.1:8766", "HTTP_PROXY": "http://127.0.0.1:8766", "https_proxy": "http://127.0.0.1:8766", "http_proxy": "http://127.0.0.1:8766", "NODE_USE_ENV_PROXY": "1", "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
			"GIT_CONFIG_GLOBAL": "/etc/loki/gitconfig", "SSH_AUTH_SOCK": "/run/loki/signing/agent.sock", "PATH": "/workspace/.loki/cargo/bin:/home/linuxbrew/.linuxbrew/bin:/usr/bin:/bin",
		} {
			values[name] = value
		}
	}
	for name, value := range c.Environment {
		values[name] = value
	}
	for name, value := range extra {
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

// ToolEnvironment is also used by service-owned Git operations so signing and
// outbound proxy configuration are identical across command entrypoints.
func (c *Controller) ToolEnvironment() []string { return c.environment(true, nil) }
func (c *Controller) execute(ctx context.Context, r Request, managed bool) (map[string]any, error) {
	if r.Action == "check" || managed && r.Action == "configured" {
		if r.Name == nil {
			return nil, fault.Error("name is required")
		}
		if managed {
			result, err := c.Manager.StartConfigured(c.Config, c.Paths, *r.Name, r.Action == "check")
			if err != nil {
				return nil, err
			}
			if r.Action == "check" {
				result["name"] = *r.Name
			}
			return result, nil
		}
		spec, ok := c.Config.Checks[*r.Name]
		if !ok {
			return nil, fault.Error("unknown check: " + *r.Name)
		}
		cwd, err := c.Paths.ResolveCWD(spec.CWD)
		if err != nil {
			return nil, err
		}
		result, err := process.Run(ctx, process.Spec{Argv: spec.Command, CWD: cwd, Env: c.environment(false, spec.Environment), Timeout: time.Duration(spec.TimeoutSeconds) * time.Second, MaxOutput: spec.MaxOutputBytes})
		return output(result, map[string]any{"name": *r.Name}), err
	}
	if managed && r.Action != "exec" && r.Action != "pnpm" {
		return nil, fault.Error("command_start action must be check, pnpm, exec, or configured")
	}
	if !slices.Contains([]string{"exec", "npm", "fnm", "just", "pnpm"}, r.Action) {
		return nil, fault.Error("command_run action must be check, npm, fnm, just, pnpm, or exec")
	}
	if err := arguments(r.Arguments, r.Action == "exec" || r.Action == "just"); err != nil {
		return nil, err
	}
	cwdRequest := r.CWD
	if r.Action == "fnm" {
		cwdRequest = "."
	}
	if r.Action != "exec" {
		if _, err := policy.Relative(cwdRequest); err != nil {
			return nil, err
		}
	}
	cwd, err := c.Paths.ResolveCWD(cwdRequest)
	if err != nil {
		return nil, err
	}
	name := r.Action
	if name == "exec" {
		if r.Executable == nil {
			return nil, fault.Error("executable is required")
		}
		name = *r.Executable
	}
	resolved, ok := policy.ExecutablePath(name)
	if custom, exists := c.Config.Executables[name]; exists && r.Action == "exec" {
		resolved, ok = custom, true
	}
	if !ok {
		return nil, fault.Error("executable is not allowed")
	}
	if r.Action == "exec" {
		if err = policy.ValidateExec(name, r.Arguments); err != nil {
			return nil, err
		}
	}
	if r.Action == "npm" && !slices.Contains([]string{"--version", "audit", "ci", "exec", "explain", "help", "install", "list", "ls", "outdated", "pack", "query", "run", "search", "test", "view", "why"}, r.Arguments[0]) {
		return nil, fault.Error("npm subcommand is not allowed")
	}
	if r.Action == "fnm" && !slices.Contains([]string{"--version", "alias", "current", "default", "env", "exec", "install", "list", "list-remote", "unalias", "uninstall", "use"}, r.Arguments[0]) {
		return nil, fault.Error("FNM subcommand is not allowed")
	}
	argv := []string{resolved}
	var version *string
	if slices.Contains([]string{"node", "npm", "pnpm", "just", "actions-up"}, name) {
		v, err := c.NodeVersion(cwd, r.NodeVersion)
		if err != nil {
			return nil, err
		}
		version = &v
		fnm, _ := policy.ExecutablePath("fnm")
		argv = []string{fnm, "exec", "--using", v}
		if name == "pnpm" {
			argv = append(argv, "corepack", "pnpm")
		} else {
			argv = append(argv, resolved)
		}
	}
	argv = append(argv, r.Arguments...)
	metadata := map[string]any{}
	if r.Action == "exec" {
		if c.Git == nil {
			return nil, errors.New("command checkpoint service is unavailable")
		}
		relative, _ := filepath.Rel(c.Paths.Root(), cwd)
		checkpoint, err := c.Git.Checkpoint(ctx, relative)
		if err != nil {
			return nil, err
		}
		metadata["executable"], metadata["node_version"], metadata["checkpoint"] = name, version, checkpoint
	} else if r.Action != "fnm" {
		metadata["node_version"] = version
	}
	if r.Action == "npm" || r.Action == "fnm" {
		metadata["subcommand"] = r.Arguments[0]
	}
	timeout := min(max(r.Timeout, 1), 1800)
	maximum := c.Config.MaxOutputBytes
	if managed {
		timeout = min(max(r.Timeout, 30), 43200)
		maximum = 10485760
	}
	spec := process.Spec{Argv: argv, CWD: cwd, Env: c.environment(true, nil), Timeout: time.Duration(timeout) * time.Second, MaxOutput: maximum}
	if managed {
		result, err := c.Manager.Start(process.StartSpec{Name: name, Spec: spec})
		if err != nil {
			return nil, err
		}
		for key, value := range metadata {
			result[key] = value
		}
		return result, nil
	}
	result, err := process.Run(ctx, spec)
	return output(result, metadata), err
}
func output(r process.Result, metadata map[string]any) map[string]any {
	metadata["exit_code"], metadata["output"], metadata["truncated"] = r.ExitCode, r.Output, r.Truncated
	if r.TimedOut {
		metadata["timed_out"] = true
	}
	return metadata
}
func (c *Controller) Run(ctx context.Context, r Request) (map[string]any, error) {
	return c.execute(ctx, r, false)
}
func (c *Controller) Start(ctx context.Context, r Request) (map[string]any, error) {
	return c.execute(ctx, r, true)
}
