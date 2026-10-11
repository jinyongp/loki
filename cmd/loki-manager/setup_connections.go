package main

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"charm.land/huh/v2"

	"loki/internal/management"
	"loki/internal/tools"
)

func setupConnections(ctx context.Context, host setupMenuHost, report management.Report, input, terminal *os.File) (bool, error) {
	config, err := defaultCodexConfig()
	if err != nil {
		return false, err
	}
	options := []huh.Option[string]{huh.NewOption("Connect Codex", "codex"), huh.NewOption("Inspect client connections", "status"), huh.NewOption("Disconnect Codex (keep tools and data)", "disconnect")}
	if runtime.GOOS == "windows" && supportsManagedTunnel(host.selected) && report.Target.Mode == tools.Full && !host.explicit {
		options = append(options, huh.NewOption("Manage OpenAI tunnel", "openai"))
	}
	options = append(options, huh.NewOption("Back", ""))
	choice := "codex"
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Client connections").Description("Execution host: " + setupHostLabel(host.selected) + "\nCodex configuration on this computer: " + config).Options(options...).Value(&choice)))
	aborted, err := runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted || choice == "" {
		return false, err
	}
	if choice == "status" {
		return false, showClientConnections(host.frontend, terminal)
	}
	if choice == "disconnect" {
		confirmed := false
		form = huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Disconnect Loki from Codex?").Description("Remove Loki-managed entries from " + config + ". Other MCP entries, installed tools and user data are retained.").Affirmative("Disconnect").Negative("Back").Value(&confirmed)))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted || !confirmed {
			return false, err
		}
		return true, disconnectCodex(config, terminal)
	}
	if choice == "openai" {
		action := "setup"
		form = huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("OpenAI tunnel").Description("Host: "+setupHostLabel(host.selected)).Options(huh.NewOption("Configure tunnel", "setup"), huh.NewOption("Inspect tunnel", "status"), huh.NewOption("Start tunnel", "start"), huh.NewOption("Stop tunnel", "stop"), huh.NewOption("Remove tunnel configuration and credentials", "remove"), huh.NewOption("Back", "")).Value(&action)))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted || action == "" {
			return false, err
		}
		if action == "remove" {
			confirmed := false
			form = huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Remove this OpenAI tunnel connection?").Description("Remove this host's connection settings and runtime key. Tool programs and data are retained.").Affirmative("Remove connection").Negative("Back").Value(&confirmed)))
			aborted, err = runSetupForm(ctx, form, input, terminal)
			if err != nil || aborted || !confirmed {
				return false, err
			}
		}
		return action != "status", runConnections(ctx, host.frontend, []string{action, "openai"}, input, terminal, terminal)
	}
	installed, enabled := setupToolCounts(report)
	if installed == 0 || enabled == 0 {
		return false, fmt.Errorf("install and enable a tool with Manage tools before connecting a client")
	}
	workspace := ""
	if report.Target.Mode == tools.ProjectHost {
		if host.selected == nil || !host.selected.Remote() {
			workspace, _ = os.Getwd()
		}
		form = huh.NewForm(huh.NewGroup(huh.NewInput().Title("Project directory on " + setupHostLabel(host.selected)).Description("Use an absolute existing directory. The browser uses this project's files.").Value(&workspace).Validate(func(value string) error {
			absolute := filepath.IsAbs(value)
			if host.selected != nil && host.selected.Remote() {
				absolute = path.IsAbs(value)
			}
			if !absolute || strings.TrimSpace(value) == "" {
				return fmt.Errorf("enter an absolute directory on the execution host")
			}
			return nil
		})))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted {
			return false, err
		}
	}
	confirmed := false
	form = huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Connect Codex to " + setupHostLabel(host.selected) + "?").Description("Update Loki's MCP entries in " + config + ". Other client settings are retained. Reopen the Codex project afterward.").Affirmative("Connect").Negative("Back").Value(&confirmed)))
	aborted, err = runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted || !confirmed {
		return false, err
	}
	arguments := []string{"codex", "--config", config}
	if workspace != "" {
		arguments = append(arguments, "--workspace", workspace)
	}
	if host.selected != nil && host.selected.Remote() {
		return true, connectRemoteCodex(ctx, *host.selected, arguments, terminal, terminal)
	}
	return true, connectCodex(host.frontend, arguments, terminal, terminal)
}

// Persistent WSL tunnel startup is owned only for Loki's dedicated distribution.
// User-managed WSL remains available through a direct Codex connection.
func supportsManagedTunnel(selected *management.ExecutionSelection) bool {
	return selected != nil && (selected.Host.Kind == "ssh" || selected.Host.Kind == "wsl" && selected.Owned)
}
