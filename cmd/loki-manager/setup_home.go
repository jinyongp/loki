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

func prepareSetupMenuHost(ctx context.Context, host *setupMenuHost, prefix *[]string, diagnostics io.Writer) (bool, error) {
	if host.selected == nil || !host.selected.Remote() {
		return false, nil
	}
	previous := *host.selected
	prepared, err := ensureSetupExecutionHost(ctx, host.frontend, previous, !host.explicit, diagnostics)
	if err != nil {
		return false, err
	}
	metadataSaved := !host.explicit
	if host.explicit && previous != prepared {
		if record, lookupErr := host.frontend.HostForTarget(previous); lookupErr == nil {
			err = host.frontend.UpdateHostTarget(record.ID, previous, prepared)
			metadataSaved = err == nil
		}
	}
	if err != nil {
		return false, err
	}
	host.selected = &prepared
	if host.explicit {
		*prefix = setupBoundArguments(host.frontend, prepared)
	}
	return prepared != previous && (metadataSaved || prepared.Command != previous.Command), nil
}

func runSetupMenu(ctx context.Context, prefix []string, host setupMenuHost, input *os.File, out, diagnostics io.Writer) error {
	terminal, interactive := setupTerminal(input, diagnostics)
	if !interactive {
		return runToolsMenu(ctx, prefix, host, input, out, diagnostics)
	}
	session := &setupSession{}
	ctx = context.WithValue(ctx, setupSessionKey{}, session)
	defer session.print(terminal)
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
		var report *management.Report
		var inspectErr error
		if host.selected == nil || !host.selected.Remote() {
			report, inspectErr = inspectOperatorHost(ctx, host.frontend, host.selected, diagnostics, false)
		}
		description := "Host: " + setupHostLabel(host.selected)
		if inspectErr != nil {
			description += "\nHost unavailable. Check access or choose Change execution host."
			description += "\n" + shortDiagnostic(inspectErr.Error())
		} else if report == nil {
			description += "\nTool state is checked when you choose an action."
		} else {
			installed, enabled := setupToolCounts(*report)
			description += fmt.Sprintf("\nTools: %d installed · %d enabled. Runtime readiness: run Check readiness.", installed, enabled)
		}
		options := []huh.Option[string]{huh.NewOption("Manage tools — install, enable, disable or uninstall", "tools"), huh.NewOption("Client connections — connect Codex or manage a tunnel", "connections"), huh.NewOption("Check readiness — prepare this host and diagnose tools", "doctor")}
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
			session.print(terminal)
			return success(out, "Setup closed.", map[string]any{"changed": changed})
		}
		if choice == "tools" || choice == "doctor" {
			var updated bool
			err = setupWorking(terminal, "Preparing execution host", setupHostLabel(host.selected), func(log io.Writer) error {
				var err error
				updated, err = prepareSetupMenuHost(ctx, &host, &prefix, log)
				if err == nil && choice == "tools" {
					report, err = inspectOperatorHost(ctx, host.frontend, host.selected, log, false)
				}
				if err == nil && choice == "doctor" {
					err = prepareInstalledBrowserHost(ctx, host, input, log)
				}
				return err
			})
			changed = changed || updated
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := showSetupResult(ctx, "Host preparation — needs attention", setupErrorSummary(err)+"\nClient connections and Change execution host remain available.", setupErrorDetails(err), input, terminal); err != nil {
					return err
				}
				continue
			}
		}
		switch choice {
		case "host":
			_, updated, selectErr := chooseSetupHost(ctx, host.frontend, input, diagnostics, false)
			changed = changed || updated
			err = selectErr
		case "tools":
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
			if viewErr := showSetupResult(ctx, "Action — could not finish", setupErrorSummary(err)+"\nSaved changes are retained. Retry or choose another action.", setupErrorDetails(err), input, terminal); viewErr != nil {
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

func installSetupTools(ctx context.Context, report management.Report, hostLabel string, input, terminal *os.File, invoke func([]string, io.Writer) error) (bool, error) {
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
	err = setupWorking(terminal, "Installing selected tools", hostLabel, func(log io.Writer) error {
		return invoke(append(command, selected...), log)
	})
	return err == nil, err
}
