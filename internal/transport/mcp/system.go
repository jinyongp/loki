package mcptransport

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
	Config                                      config.Config
	Policy                                      controlpolicy.Generation
	Paths                                       *policy.Workspace
	Started                                     time.Time
	RuntimeSocket, BrowserSocket, SigningSocket string
	Artifacts, Previews                         bool
	InspectPort                                 func(context.Context, int) (map[string]any, error)
	GitEnvironment                              []string
	ToolNames                                   []string
	Audit                                       *audit.Log
}

func catalogInfo(effective []string) map[string]any {
	names := append([]string(nil), effective...)
	if len(names) == 0 {
		definitions, _ := contract.CurrentDefinitions()
		names = make([]string, 0, len(definitions))
		for _, definition := range definitions {
			names = append(names, definition.Name)
		}
	}
	sort.Strings(names)
	return map[string]any{"revision": contract.CatalogRevision, "count": len(names), "tools": names, "sha256": workspace.Digest([]byte(strings.Join(names, "\n"))), "client_sync": "ChatGPT custom-app actions are a frozen snapshot; refresh the app actions and start a new chat only when the public tool schema revision changes"}
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func socketExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}
func (c *SystemController) Info(ctx context.Context) map[string]any {
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
	integrations := c.integrationStatus(ctx)
	github := integrations["github"].(map[string]any)
	browserReady := integrationIsReady(integrations, "browser")
	signingReady := integrationIsReady(integrations, "signing")
	return map[string]any{
		"name": "loki", "version": buildinfo.Version, "schema_revision": "2026-09-29.1",
		"mcp_sdk_version": sdk, "python_version": nil, "go_version": runtime.Version(),
		"uptime_seconds": uptime, "server_time": time.Now().UTC().Format(time.RFC3339Nano),
		"workspace": "/workspace", "policy_generation": c.Policy.Metadata(),
		"tool_catalog": catalogInfo(c.ToolNames), "integrations": integrations,
		"capabilities": map[string]any{
			"text_files": true, "images": []string{"gif", "jpeg", "png", "webp"},
			"temporary_image_links": c.Artifacts, "temporary_file_links": c.Artifacts,
			"workspace_bundles": c.Artifacts, "developer_output_viewer": true,
			"temporary_live_previews": c.Previews, "git_checkpoints": true,
			"file_revisions": true, "git_partial_staging": true,
			"signed_git_commits": signingReady, "secret_profiles": socketExists(c.RuntimeSocket),
			"devtools":             map[string]any{"direct_cli": true, "project_state": true, "task_queues": true, "configured_commands": true, "managed_processes": true, "workspace_ports": true},
			"secret_management":    map[string]any{"vault": "AES-GCM", "opaque_staged_imports": true, "profile_lifecycle": true, "direct_value_access": false, "brokered_process_start": true},
			"agent_skills":         map[string]any{"revision": "2026-09-14.1", "installed": []string{"devtools"}},
			"github":               github,
			"github_https":         true,
			"structured_browser":   browserReady,
			"browser_devtools":     browserReady,
			"browser_tool_catalog": map[string]any{"revision": "2026-09-03.1", "count": len(browserTools), "tools": browserTools},
		},
		"limits": map[string]any{"max_file_bytes": c.Config.MaxFileBytes, "max_write_bytes": c.Config.MaxWriteBytes, "max_image_bytes": workspace.MaxImageBytes, "max_shared_file_bytes": workspace.MaxSharedBytes, "max_bundle_files": 512},
	}
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
	readable := accessible(c.Paths.Root(), unix.R_OK)
	writable := accessible(c.Paths.Root(), unix.W_OK)
	auditWritable := accessible(filepath.Dir(c.Config.AuditLog), unix.W_OK)
	coreHealthy := readable && writable && auditWritable
	integrations := c.integrationStatus(ctx)
	github := integrations["github"].(map[string]any)
	signing := integrations["signing"].(map[string]any)
	browser := integrations["browser"].(map[string]any)
	return map[string]any{
		"healthy":      coreHealthy,
		"core_healthy": coreHealthy,
		"workspace":    map[string]any{"readable": readable, "writable": writable},
		"audit_log":    map[string]any{"directory_writable": auditWritable},
		"integrations": integrations,
		"github": map[string]any{
			"configured": github["configured"], "target_count": github["target_count"],
			"ready": github["ready"], "state": github["state"], "protocol": "https",
		},
		"git_signing": map[string]any{
			"identity_configured": signing["identity_configured"], "format": signing["format"],
			"commit_signing_required": signing["commit_signing_required"],
			"public_key_available":    signing["public_key_available"],
			"agent_socket_available":  signing["agent_socket_available"],
			"ready":                   signing["ready"], "state": signing["state"],
		},
		"repositories": repositories,
		"tool_catalog": catalogInfo(c.ToolNames),
		"browser": map[string]any{
			"socket_available": browser["ready"], "ready": browser["ready"], "state": browser["state"],
			"catalog_revision": "2026-09-03.1", "expected_tool_count": len(browserTools), "expected_tools": browserTools,
		},
	}
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
			return mcpserver.Object(c.Info(ctx))
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
