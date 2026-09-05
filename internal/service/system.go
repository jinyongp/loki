package service

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"
	"loki/internal/buildinfo"
	"loki/internal/config"
	"loki/internal/contract"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/process"
	"loki/internal/workspace"
)

var browserTools = []string{"browser_session", "browser_observe", "browser_interact", "browser_screenshot", "browser_save_screenshot", "browser_share_screenshot"}

type SystemController struct {
	Config                       config.Config
	Paths                        *policy.Workspace
	Processes                    *process.Manager
	Started                      time.Time
	RuntimeSocket, BrowserSocket string
	Artifacts, Previews          bool
	InspectPort                  func(context.Context, int) (map[string]any, error)
	GitEnvironment               []string
}

func catalogInfo() map[string]any {
	baseline, _ := contract.Baseline()
	definitions, _ := baseline.Definitions()
	names := make([]string, 0, len(definitions))
	for _, d := range definitions {
		names = append(names, d.Name)
	}
	return map[string]any{"revision": "2026-09-04.4", "count": len(names), "tools": names, "sha256": workspace.Digest([]byte(strings.Join(names, "\n"))), "client_sync": "ChatGPT custom-app actions are a frozen snapshot; refresh the app actions and start a new chat when this revision changes"}
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func socketExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}
func keys[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (c *SystemController) Info() map[string]any {
	sdk := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/modelcontextprotocol/go-sdk" {
				sdk = dep.Version
			}
		}
	}
	uptime := 0.0
	if !c.Started.IsZero() {
		uptime = math.Round(time.Since(c.Started).Seconds()*1000) / 1000
	}
	return map[string]any{"name": "loki", "version": buildinfo.Version, "schema_revision": "2026-09-04.1", "mcp_sdk_version": sdk, "python_version": nil, "go_version": runtime.Version(), "uptime_seconds": uptime, "workspace": "/workspace", "tool_catalog": catalogInfo(),
		"capabilities": map[string]any{
			"text_files": true, "images": []string{"gif", "jpeg", "png", "webp"}, "temporary_image_links": c.Artifacts, "temporary_file_links": c.Artifacts, "workspace_bundles": c.Artifacts, "developer_output_viewer": true, "shared_project_state": true, "temporary_live_previews": c.Previews, "command_execution": true, "managed_processes": true, "workspace_port_control": true, "go_toolchain": true, "rust_toolchain": true, "git_checkpoints": true, "file_revisions": true, "git_partial_staging": true, "signed_git_commits": true, "secret_profiles": socketExists(c.RuntimeSocket),
			"secret_management": map[string]any{"opaque_staged_imports": true, "agent_profile_lifecycle": true, "direct_value_access": false, "action_registration": "root-only"},
			"agent_skills":      map[string]any{"revision": "2026-09-03.1", "builtin_root": "builtin", "shared_root": ".agents/skills", "project_override": true, "precedence": []string{"project", "shared", "builtin"}, "dynamic_catalog": true},
			"github_https":      true, "structured_browser": exists(c.BrowserSocket), "browser_devtools": exists(c.BrowserSocket), "browser_tool_catalog": map[string]any{"revision": "2026-09-03.1", "count": len(browserTools), "tools": browserTools},
		}, "limits": map[string]any{"max_file_bytes": c.Config.MaxFileBytes, "max_write_bytes": c.Config.MaxWriteBytes, "max_image_bytes": workspace.MaxImageBytes, "max_shared_file_bytes": workspace.MaxSharedBytes, "max_bundle_files": 512, "max_processes": c.Config.MaxProcesses}}
}
func (c *SystemController) git(ctx context.Context, args ...string) string {
	r, err := process.Run(ctx, process.Spec{Argv: append([]string{"/usr/bin/git"}, args...), CWD: c.Paths.Root(), Env: c.GitEnvironment, Timeout: 10 * time.Second, MaxOutput: 4096})
	if err != nil || r.ExitCode != 0 || r.Truncated {
		return ""
	}
	return strings.TrimSpace(r.Output)
}
func (c *SystemController) Workspace(ctx context.Context) map[string]any {
	info, err := os.Lstat(filepath.Join(c.Paths.Root(), ".git"))
	repository := err == nil && info.IsDir()
	var branch any
	if repository {
		if value := c.git(ctx, "rev-parse", "--abbrev-ref", "HEAD"); value != "" {
			branch = value
		}
	}
	executables := map[string]bool{}
	for _, name := range policy.ExecutableNames() {
		executables[name] = true
	}
	for name := range c.Config.Executables {
		executables[name] = true
	}
	return map[string]any{"root": "/workspace", "repository": repository, "branch": branch, "checks": keys(c.Config.Checks), "processes": keys(c.Config.Processes), "executables": keys(executables), "running_processes": c.Processes.Usage()["total"], "limits": map[string]any{"max_file_bytes": c.Config.MaxFileBytes, "max_write_bytes": c.Config.MaxWriteBytes, "max_patch_bytes": c.Config.MaxPatchBytes, "max_patch_files": c.Config.MaxPatchFiles, "max_processes": c.Config.MaxProcesses}}
}
func (c *SystemController) Diagnostics(ctx context.Context) map[string]any {
	accessible := func(path string, mode uint32) bool {
		return unix.Faccessat(unix.AT_FDCWD, path, mode, unix.AT_EACCESS) == nil
	}
	toolchain := map[string]bool{}
	healthy := true
	for _, name := range []string{"fnm", "fd", "gh", "git", "go", "just", "jq", "hyperfine", "actionlint", "actions-up", "python3", "rg", "rustup", "cargo", "rustc"} {
		path, _ := policy.ExecutablePath(name)
		toolchain[name] = accessible(path, unix.X_OK)
		healthy = healthy && toolchain[name]
	}
	repositories := []string{}
	entries, _ := os.ReadDir(c.Paths.Root())
	for _, entry := range entries {
		if entry.IsDir() && exists(filepath.Join(c.Paths.Root(), entry.Name(), ".git")) {
			repositories = append(repositories, entry.Name())
		}
	}
	sort.Strings(repositories)
	configValue := func(key string) string { return c.git(ctx, "config", "--global", "--includes", "--get", key) }
	identity := configValue("user.name") != "" && configValue("user.email") != ""
	format := configValue("gpg.format")
	required := strings.ToLower(configValue("commit.gpgsign")) == "true"
	publicKey := exists("/home/runner/.ssh/id_ed25519.pub")
	agent := socketExists("/run/loki/signing/agent.sock")
	var signingFormat any
	if format != "" {
		signingFormat = format
	}
	readable, writable := accessible(c.Paths.Root(), unix.R_OK), accessible(c.Paths.Root(), unix.W_OK)
	return map[string]any{"healthy": healthy && readable && writable && identity && format == "ssh" && required && publicKey && agent,
		"workspace": map[string]any{"readable": readable, "writable": writable}, "audit_log": map[string]any{"directory_writable": accessible(filepath.Dir(c.Config.AuditLog), unix.W_OK)}, "github": map[string]any{"config_mounted": exists("/home/runner/.config/gh/hosts.yml"), "protocol": "https"},
		"git_signing": map[string]any{"identity_configured": identity, "format": signingFormat, "commit_signing_required": required, "public_key_available": publicKey, "agent_socket_available": agent}, "toolchain": toolchain, "repositories": repositories, "configured_executables": keys(c.Config.Executables), "tool_catalog": catalogInfo(),
		"browser": map[string]any{"socket_available": exists(c.BrowserSocket), "catalog_revision": "2026-09-03.1", "expected_tool_count": len(browserTools), "expected_tools": browserTools}}
}
func SystemHandler(c *SystemController) mcpserver.Handler {
	return mcpserver.Typed(func(ctx context.Context, r struct {
		Action string
		Port   *int
	}) (*mcp.CallToolResult, error) {
		switch r.Action {
		case "server":
			return mcpserver.Object(c.Info())
		case "workspace":
			return mcpserver.Object(c.Workspace(ctx))
		case "diagnostics":
			return mcpserver.Object(c.Diagnostics(ctx))
		case "port":
			port, err := mcpserver.Require(r.Port, "port")
			if err != nil {
				return nil, err
			}
			if c.InspectPort == nil {
				return nil, fault.Error("port inspection is unavailable")
			}
			return objectResult(c.InspectPort(ctx, port))
		}
		return nil, fault.Error("system_inspect action must be server, diagnostics, workspace, or port")
	})
}
