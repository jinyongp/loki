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
	"loki/internal/audit"
	"loki/internal/buildinfo"
	"loki/internal/config"
	"loki/internal/contract"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/process"
	"loki/internal/work/workspace"
)

var browserTools = []string{"browser_session", "browser_observe", "browser_interact", "browser_screenshot", "browser_save_screenshot", "browser_share_screenshot"}

type SystemController struct {
	Config                       config.Config
	Policy                       controlpolicy.Generation
	Paths                        *policy.Workspace
	Started                      time.Time
	RuntimeSocket, BrowserSocket string
	Artifacts, Previews          bool
	InspectPort                  func(context.Context, int) (map[string]any, error)
	GitEnvironment               []string
	Audit                        *audit.Log
}

func catalogInfo() map[string]any {
	definitions, _ := contract.CurrentDefinitions()
	names := make([]string, 0, len(definitions))
	for _, d := range definitions {
		names = append(names, d.Name)
	}
	return map[string]any{"revision": contract.CatalogRevision, "count": len(names), "tools": names, "sha256": workspace.Digest([]byte(strings.Join(names, "\n"))), "client_sync": "ChatGPT custom-app actions are a frozen snapshot; refresh the app actions and start a new chat when this revision changes"}
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func socketExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
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
	return map[string]any{"name": "loki", "version": buildinfo.Version, "schema_revision": "2026-09-15.2", "mcp_sdk_version": sdk, "python_version": nil, "go_version": runtime.Version(), "uptime_seconds": uptime, "server_time": time.Now().UTC().Format(time.RFC3339Nano), "workspace": "/workspace", "policy_generation": c.Policy.Metadata(), "tool_catalog": catalogInfo(),
		"capabilities": map[string]any{
			"text_files": true, "images": []string{"gif", "jpeg", "png", "webp"}, "temporary_image_links": c.Artifacts, "temporary_file_links": c.Artifacts, "workspace_bundles": c.Artifacts, "developer_output_viewer": true, "temporary_live_previews": c.Previews, "git_checkpoints": true, "file_revisions": true, "git_partial_staging": true, "signed_git_commits": true, "secret_profiles": socketExists(c.RuntimeSocket),
			"devtools":          map[string]any{"direct_cli": true, "project_state": true, "task_queues": true, "configured_commands": true, "managed_processes": true, "workspace_ports": true},
			"secret_management": map[string]any{"vault": "AES-GCM", "opaque_staged_imports": true, "profile_lifecycle": true, "direct_value_access": false, "brokered_process_start": true},
			"agent_skills":      map[string]any{"revision": "2026-09-14.1", "installed": []string{"devtools"}},
			"github":            map[string]any{"configured": c.Config.GitHubAppID != 0, "target_count": len(c.Config.GitHubTargets), "authentication": "GitHub App installation tokens"},
			"github_https":      true, "structured_browser": exists(c.BrowserSocket), "browser_devtools": exists(c.BrowserSocket), "browser_tool_catalog": map[string]any{"revision": "2026-09-03.1", "count": len(browserTools), "tools": browserTools},
		}, "limits": map[string]any{"max_file_bytes": c.Config.MaxFileBytes, "max_write_bytes": c.Config.MaxWriteBytes, "max_image_bytes": workspace.MaxImageBytes, "max_shared_file_bytes": workspace.MaxSharedBytes, "max_bundle_files": 512}}
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
	return map[string]any{"root": "/workspace", "repository": repository, "branch": branch, "limits": map[string]any{"max_file_bytes": c.Config.MaxFileBytes, "max_write_bytes": c.Config.MaxWriteBytes, "max_patch_bytes": c.Config.MaxPatchBytes, "max_patch_files": c.Config.MaxPatchFiles}}
}
func (c *SystemController) Diagnostics(ctx context.Context) map[string]any {
	accessible := func(path string, mode uint32) bool {
		return unix.Faccessat(unix.AT_FDCWD, path, mode, unix.AT_EACCESS) == nil
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
	agent := socketExists("/run/loki-go/signing/agent.sock")
	var signingFormat any
	if format != "" {
		signingFormat = format
	}
	readable, writable := accessible(c.Paths.Root(), unix.R_OK), accessible(c.Paths.Root(), unix.W_OK)
	return map[string]any{"healthy": readable && writable && identity && format == "ssh" && required && publicKey && agent,
		"workspace": map[string]any{"readable": readable, "writable": writable}, "audit_log": map[string]any{"directory_writable": accessible(filepath.Dir(c.Config.AuditLog), unix.W_OK)}, "github": map[string]any{"configured": c.Config.GitHubAppID != 0, "target_count": len(c.Config.GitHubTargets), "protocol": "https"},
		"git_signing": map[string]any{"identity_configured": identity, "format": signingFormat, "commit_signing_required": required, "public_key_available": publicKey, "agent_socket_available": agent}, "repositories": repositories, "tool_catalog": catalogInfo(),
		"browser": map[string]any{"socket_available": exists(c.BrowserSocket), "catalog_revision": "2026-09-03.1", "expected_tool_count": len(browserTools), "expected_tools": browserTools}}
}
func SystemHandler(c *SystemController) mcpserver.Handler {
	type request struct {
		Action        string  `json:"action"`
		Port          *int    `json:"port"`
		Limit         *int    `json:"limit"`
		CorrelationID *string `json:"correlation_id"`
	}
	return mcpserver.Typed(func(ctx context.Context, r request) (*mcp.CallToolResult, error) {
		switch r.Action {
		case "server":
			return mcpserver.Object(c.Info())
		case "workspace":
			return mcpserver.Object(c.Workspace(ctx))
		case "diagnostics":
			return mcpserver.Object(c.Diagnostics(ctx))
		case "activity":
			limit := 20
			if r.Limit != nil {
				limit = *r.Limit
			}
			items, err := recentToolActivity(c.Audit, limit, time.Now().UTC())
			if err != nil {
				return nil, fault.Error("tool activity is unavailable")
			}
			return mcpserver.Object(map[string]any{
				"server_time": time.Now().UTC().Format(time.RFC3339Nano),
				"items":       items,
			})
		case "operation":
			correlationID, err := mcpserver.Require(r.CorrelationID, "correlation_id")
			if err != nil {
				return nil, err
			}
			item, err := toolActivityByCorrelationID(c.Audit, correlationID, time.Now().UTC())
			if err != nil {
				return nil, fault.New(fault.CodeInvalidInput, "tool activity correlation is not retained", false, "use system_inspect action=activity to inspect retained operations")
			}
			return mcpserver.Object(map[string]any{
				"server_time": time.Now().UTC().Format(time.RFC3339Nano),
				"item":        item,
			})
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
		return nil, fault.Error("system_inspect action must be server, diagnostics, workspace, activity, operation, or port")
	})
}
