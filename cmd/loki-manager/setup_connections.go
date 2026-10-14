package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	changed := false
	choice := "codex"
	for {
		options := []huh.Option[string]{huh.NewOption("Connect Codex", "codex"), huh.NewOption("Inspect client connections", "status"), huh.NewOption("Disconnect Codex (keep tools and data)", "disconnect")}
		if runtime.GOOS == "windows" && supportsManagedTunnel(host.selected) && (report.Target.Mode == tools.Full || report.Tools == nil) {
			options = append(options, huh.NewOption("Manage OpenAI tunnel", "openai"))
		}
		options = append(options, huh.NewOption("Back", ""))
		form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Client connections").Description("Execution host: " + setupHostLabel(host.selected) + "\nCodex configuration on this computer: " + config).Options(options...).Value(&choice)))
		aborted, err := runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted || choice == "" {
			return changed, err
		}
		updated, err := setupConnectionAction(ctx, host, report, choice, config, input, terminal)
		changed = changed || updated
		if err != nil {
			if ctx.Err() != nil {
				return changed, ctx.Err()
			}
			if viewErr := showSetupResult(ctx, "Client connection — needs attention", setupErrorSummary(err), setupErrorDetails(err), input, terminal); viewErr != nil {
				return changed, viewErr
			}
		}
	}
}

func setupConnectionAction(ctx context.Context, host setupMenuHost, report management.Report, choice, config string, input, terminal *os.File) (bool, error) {
	if choice == "status" {
		var result bytes.Buffer
		if err := showClientConnections(host.frontend, &result); err != nil {
			return false, err
		}
		return false, showSetupResult(ctx, "Client connections", result.String(), "", input, terminal)
	}
	if choice == "disconnect" {
		confirmed := false
		form := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Disconnect " + setupHostLabel(host.selected) + " from Codex?").Description("Remove this host's Loki-managed entries from " + config + ". Other MCP entries, installed tools and user data are retained.").Affirmative("Disconnect").Negative("Back").Value(&confirmed)))
		aborted, err := runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted || !confirmed {
			return false, err
		}
		var result setupChangedWriter
		if err := disconnectCodexTarget(host.frontend, host.selected, "", false, &commandOutput{Writer: &result, json: true}); err != nil {
			return false, err
		}
		changed := result.changed()
		title, summary := "Codex — unchanged", "No Loki-managed connection for this host was found."
		if changed {
			title, summary = "Codex — disconnected", "This host's Loki-managed connections were removed. Reload the Loki MCP connection in Codex."
		}
		return changed, showSetupResult(ctx, title, summary, result.String(), input, terminal)
	}
	if choice == "openai" {
		action := "setup"
		form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("OpenAI tunnel").Description("Host: "+setupHostLabel(host.selected)).Options(huh.NewOption("Configure tunnel", "setup"), huh.NewOption("Inspect tunnel", "status"), huh.NewOption("Start tunnel", "start"), huh.NewOption("Stop tunnel", "stop"), huh.NewOption("Remove tunnel configuration and credentials", "remove"), huh.NewOption("Back", "")).Value(&action)))
		aborted, err := runSetupForm(ctx, form, input, terminal)
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
		if err := setupWorking(terminal, "OpenAI tunnel: "+action, setupHostLabel(host.selected), func(log io.Writer) error {
			return runConnections(ctx, host.frontend, registeredConnectionArguments(host.frontend, host.selected, []string{action, "openai"}), input, io.MultiWriter(&result, log), log)
		}); err != nil {
			return false, err
		}
		return action != "status", showSetupResult(ctx, "OpenAI tunnel — "+action+" completed", shortDiagnostic(result.String()), result.String(), input, terminal)
	}
	if report.Tools == nil {
		err := setupWorking(terminal, "Checking tool configuration", setupHostLabel(host.selected), func(log io.Writer) error {
			current, err := inspectOperatorHost(ctx, host.frontend, host.selected, log, false)
			if err == nil {
				report = *current
			}
			return err
		})
		if err != nil {
			return false, err
		}
	}
	installed, enabled := setupToolCounts(report)
	if installed == 0 || enabled == 0 {
		return false, fmt.Errorf("install and enable a tool with Manage tools before connecting a client")
	}
	confirmed := false
	form := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Connect Codex to " + setupHostLabel(host.selected) + "?").Description(setupConnectionDescription(report, config)).Affirmative("Connect").Negative("Back").Value(&confirmed)))
	aborted, err := runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted || !confirmed {
		return false, err
	}
	arguments := []string{"codex", "--config", config}
	var result setupChangedWriter
	output := &commandOutput{Writer: &result, json: true}
	err = setupWorking(terminal, "Connecting Codex", setupHostLabel(host.selected), func(log io.Writer) error {
		if host.selected != nil && host.selected.Remote() {
			return connectCodexOnHost(ctx, host.frontend, host.selected, arguments, output, log)
		}
		return connectCodex(host.frontend, arguments, output, log)
	})
	if err != nil {
		return false, err
	}
	changed := result.changed()
	title, summary := "Codex — unchanged", "This host's Loki connection is already configured."
	if changed {
		title, summary = "Codex — connected", "Connection settings saved. Reload the Loki MCP connection in Codex to load them."
	}
	return changed, showSetupResult(ctx, title, "Host: "+setupHostLabel(host.selected)+"\n"+summary+"\nTool readiness is checked separately with Check readiness.", result.String(), input, terminal)
}

func setupConnectionDescription(report management.Report, config string) string {
	var active []string
	for _, name := range publicToolNames {
		observed := report.Tools[tools.ID(name)]
		if observed.Installed && observed.Enabled {
			active = append(active, name)
		}
	}
	description := "Loki tools: " + strings.Join(active, ", ") + "\nRegister one Loki MCP connection in " + config + ". This connection keeps this host even if Loki's default changes. Reload the Loki MCP connection in Codex afterward."
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
