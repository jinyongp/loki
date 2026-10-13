package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
	"golang.org/x/term"

	"loki/internal/management"
	"loki/internal/tools"
)

type setupChangedWriter struct{ bytes.Buffer }

func (w *setupChangedWriter) changed() bool {
	var result struct {
		Changed bool `json:"changed"`
	}
	return json.Unmarshal(w.Bytes(), &result) == nil && result.Changed
}

func setupTerminal(input *os.File, diagnostics io.Writer) (*os.File, bool) {
	terminal, ok := diagnostics.(*os.File)
	return terminal, ok && term.IsTerminal(int(input.Fd())) && term.IsTerminal(int(terminal.Fd())) && os.Getenv("TERM") != "dumb"
}

func runSetupMenu(ctx context.Context, prefix []string, host setupMenuHost, input *os.File, out, diagnostics io.Writer) error {
	terminal, interactive := setupTerminal(input, diagnostics)
	if !interactive {
		return runToolsMenu(ctx, prefix, host, input, out, diagnostics)
	}
	changed := false
	if host.selected == nil && !host.explicit {
		selected, updated, err := chooseSetupHost(ctx, host.frontend, input, diagnostics, false)
		if err != nil {
			return err
		}
		if !updated {
			return success(out, "Setup cancelled. No changes made.", map[string]any{"state": "cancelled", "changed": false})
		}
		host.selected, changed = selected, true
	} else if host.selected != nil && host.selected.Remote() {
		setupWorking(terminal, "Preparing execution host", setupHostLabel(host.selected))
		prepared, err := ensureSetupExecutionHost(ctx, host.frontend, *host.selected, !host.explicit, diagnostics)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if viewErr := showSetupResult(ctx, "Host preparation — needs attention", shortDiagnostic(err.Error())+"\nClient connections and Change execution host remain available.", err.Error(), input, terminal); viewErr != nil {
				return viewErr
			}
		} else {
			changed = changed || prepared != *host.selected
			host.selected = &prepared
			if host.explicit {
				prefix = setupExecutionArguments(prepared)
			}
		}
	}
	if err := prepareInstalledBrowserHost(ctx, host, input, diagnostics); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if viewErr := showSetupResult(ctx, "Browser preparation — needs attention", shortDiagnostic(err.Error()), err.Error(), input, terminal); viewErr != nil {
			return viewErr
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !host.explicit {
			var err error
			host.selected, err = host.frontend.ExecutionSelection()
			if err != nil {
				return err
			}
		}
		report, inspectErr := inspectOperatorHost(ctx, host.frontend, host.selected, diagnostics, false)
		description := "Host: " + setupHostLabel(host.selected)
		if inspectErr != nil {
			description += "\nHost unavailable. Check access or choose Change execution host."
			description += "\n" + shortDiagnostic(inspectErr.Error())
		} else {
			installed, enabled := setupToolCounts(*report)
			description += fmt.Sprintf("\nTools: %d installed · %d enabled. Runtime readiness: run Check readiness.", installed, enabled)
		}
		options := []huh.Option[string]{huh.NewOption("Manage tools — install, enable, disable or uninstall", "tools"), huh.NewOption("Client connections — connect Codex or manage a tunnel", "connections"), huh.NewOption("Check readiness — diagnose tools on this host", "doctor")}
		if !host.explicit {
			options = append(options, huh.NewOption("Change execution host", "host"))
		}
		options = append(options, huh.NewOption("Done", ""))
		choice := "tools"
		form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Loki setup").Description(description).Options(options...).Value(&choice)))
		aborted, err := runSetupForm(ctx, form, input, terminal)
		if err != nil {
			return err
		}
		if aborted || choice == "" {
			return success(out, "Setup closed. Saved changes are retained. Run 'loki status' to inspect them.", map[string]any{"changed": changed})
		}
		switch choice {
		case "host":
			_, updated, selectErr := chooseSetupHost(ctx, host.frontend, input, diagnostics, false)
			changed = changed || updated
			err = selectErr
		case "tools":
			if inspectErr != nil {
				continue
			}
			var result setupChangedWriter
			err = runToolsMenu(ctx, prefix, host, input, &commandOutput{Writer: &result, json: true}, diagnostics)
			changed = changed || result.changed()
		case "connections":
			if report == nil {
				report = &management.Report{}
			}
			updated, connectErr := setupConnections(ctx, host, *report, input, terminal)
			changed = changed || updated
			err = connectErr
		case "doctor":
			err = setupReadiness(ctx, host, input, terminal)
		}
		if err != nil {
			if viewErr := showSetupResult(ctx, "Action — could not finish", shortDiagnostic(err.Error())+"\nSaved changes are retained. Retry or choose another action.", err.Error(), input, terminal); viewErr != nil {
				return viewErr
			}
		}
	}
}

func setupToolCounts(report management.Report) (installed, enabled int) {
	for _, name := range publicToolNames {
		state := report.Tools[tools.ID(name)]
		if state.Installed {
			installed++
			if state.Enabled {
				enabled++
			}
		}
	}
	return
}

func toolSupportedOnHost(name string, target tools.Target) bool {
	return target.OS == "linux" || name == "browser" && (target.OS != "windows" || target.Arch != "arm64")
}

func installSetupTools(ctx context.Context, report management.Report, hostLabel string, input, terminal *os.File, invoke func([]string) error) (bool, error) {
	options := []huh.Option[string]{}
	for _, name := range publicToolNames {
		if !report.Tools[tools.ID(name)].Installed && toolSupportedOnHost(name, report.Target) {
			options = append(options, huh.NewOption(name+" — "+setupToolDescriptions[name], name))
		}
	}
	if len(options) == 0 {
		return false, showSetupResult(ctx, "Installation — nothing to add", "All supported tools are installed on "+hostLabel+".", "", input, terminal)
	}
	var selected []string
	form := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().Title("Install tools").Description("Host: " + hostLabel + "\nSpace select · Enter continue · Esc back").Options(options...).Value(&selected)))
	aborted, err := runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted || len(selected) == 0 {
		return false, err
	}
	action := "install-enable"
	form = huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(strings.Join(selected, ", ")).Description("Host: "+hostLabel).Options(huh.NewOption("Install and enable", "install-enable"), huh.NewOption("Install only (disabled)", "install"), huh.NewOption("Back", "")).Value(&action)))
	aborted, err = runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted || action == "" {
		return false, err
	}
	command := []string{"tools", "install"}
	if action == "install-enable" {
		command = []string{"setup"}
	}
	setupWorking(terminal, "Installing selected tools", hostLabel)
	return true, invoke(append(command, selected...))
}
