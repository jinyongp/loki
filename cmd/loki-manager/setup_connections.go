package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
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
	if runtime.GOOS == "windows" && supportsManagedTunnel(host.selected) && report.Target.Mode == tools.Full {
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
		var result bytes.Buffer
		if err := showClientConnections(host.frontend, &result); err != nil {
			return false, err
		}
		return false, showSetupResult(ctx, "Client connections", result.String(), "", input, terminal)
	}
	if choice == "disconnect" {
		confirmed := false
		form = huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Disconnect Loki from Codex?").Description("Remove this host’s Loki-managed entries from " + config + ". Other MCP entries, installed tools and user data are retained.").Affirmative("Disconnect").Negative("Back").Value(&confirmed)))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted || !confirmed {
			return false, err
		}
		var result bytes.Buffer
		if err := disconnectCodexTarget(host.frontend, host.selected, "", false, &result); err != nil {
			return false, err
		}
		return true, showSetupResult(ctx, "Codex — disconnected", result.String(), "", input, terminal)
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
		var result bytes.Buffer
		if err := runConnections(ctx, host.frontend, registeredConnectionArguments(host.frontend, host.selected, []string{action, "openai"}), input, &result, terminal); err != nil {
			return false, err
		}
		return action != "status", showSetupResult(ctx, "OpenAI tunnel — "+action+" completed", shortDiagnostic(result.String()), result.String(), input, terminal)
	}
	installed, enabled := setupToolCounts(report)
	if installed == 0 || enabled == 0 {
		return false, fmt.Errorf("install and enable a tool with Manage tools before connecting a client")
	}
	confirmed := false
	form = huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Connect Codex to " + setupHostLabel(host.selected) + "?").Description(setupConnectionDescription(report, config)).Affirmative("Connect").Negative("Back").Value(&confirmed)))
	aborted, err = runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted || !confirmed {
		return false, err
	}
	arguments := []string{"codex", "--config", config}
	setupWorking(terminal, "Connecting Codex", setupHostLabel(host.selected))
	var result, diagnostics bytes.Buffer
	if host.selected != nil && host.selected.Remote() {
		err = connectCodexOnHost(ctx, host.frontend, host.selected, arguments, &result, &diagnostics)
	} else {
		err = connectCodex(host.frontend, arguments, &result, &diagnostics)
	}
	if err != nil {
		return false, fmt.Errorf("%w\n%s", err, diagnostics.String())
	}
	return true, showSetupResult(ctx, "Codex — connected", "Host: "+setupHostLabel(host.selected)+"\nConnection settings saved. Reopen the Codex project to load Loki.\nTool readiness is checked separately with Check readiness.", result.String(), input, terminal)
}

func setupConnectionDescription(report management.Report, config string) string {
	var active []string
	for _, name := range publicToolNames {
		observed := report.Tools[tools.ID(name)]
		if observed.Installed && observed.Enabled {
			active = append(active, name)
		}
	}
	description := "Loki tools: " + strings.Join(active, ", ") + "\nRegister one Loki MCP connection in " + config + ". This connection keeps this host even if Loki's default changes. Reopen the Codex project afterward."
	if report.Target.Mode == tools.ProjectHost {
		description += "\nBrowser files use a private temporary directory. To grant project file access, connect with --workspace PATH."
	}
	return description
}

// Persistent WSL tunnel startup is owned only for Loki's dedicated distribution.
// User-managed WSL remains available through a direct Codex connection.
func supportsManagedTunnel(selected *management.ExecutionSelection) bool {
	return selected != nil && (selected.Host.Kind == "ssh" || selected.Host.Kind == "wsl" && selected.Owned)
}
