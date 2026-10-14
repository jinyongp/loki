package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"charm.land/huh/v2"
	"golang.org/x/term"

	"loki/internal/management"
	"loki/internal/tools"
)

type setupAction struct {
	label string
	name  string
}

func setupActions(state tools.State) []setupAction {
	if !state.Installed {
		return []setupAction{{"Install and enable", "install-enable"}, {"Install only", "install"}}
	}
	if state.Enabled {
		return []setupAction{{"Disable (keep installed)", "disable"}, {"Enable / retry start", "enable"}, {"Uninstall (keep user data)", "uninstall"}}
	}
	return []setupAction{{"Enable", "enable"}, {"Uninstall (keep user data)", "uninstall"}}
}

func setupStateLabel(state tools.State) string {
	if !state.Installed {
		return "not installed"
	}
	if state.Enabled {
		return "installed | enabled"
	}
	return "installed | disabled"
}

func setupToolActions(id string, state tools.State, mode tools.Mode) []setupAction {
	actions := setupActions(state)
	if state.Installed && id == "browser" {
		actions = append([]setupAction{{"Optional features — video recording, PDF and more", "features"}}, actions...)
	}
	if state.Installed && state.Enabled && mode == tools.Full {
		if id == "git" {
			actions = append(actions, setupAction{"Configure commit signing", "configure"})
		} else if id == "github" {
			actions = append(actions, setupAction{"Configure GitHub access", "configure"})
		}
	}
	return actions
}

// Use the command router so the menu reads and changes the selected execution
// host, including protected Linux hosts and remembered Windows WSL hosts.
func runToolsMenu(ctx context.Context, prefix []string, host setupMenuHost, input *os.File, out, diagnostics io.Writer) error {
	if !term.IsTerminal(int(input.Fd())) {
		line, err := readSetupLine(input, 4096)
		if err != nil && err != io.EOF {
			return err
		}
		if strings.TrimSpace(line) == "" {
			return success(out, "Setup cancelled.", map[string]any{"state": "cancelled", "changed": false})
		}
		return fmt.Errorf("interactive setup needs a terminal; use 'loki setup TOOL...' or 'loki tools install TOOL' for scripts")
	}
	terminal, ok := diagnostics.(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(terminal.Fd())) || os.Getenv("TERM") == "dumb" {
		return fmt.Errorf("interactive setup needs a terminal; use 'loki setup TOOL...' or 'loki tools install TOOL' for scripts")
	}
	invoke := func(command []string, destination io.Writer) error {
		return run(ctx, append(append([]string{}, prefix...), command...), destination, diagnostics)
	}
	load := func() (management.Report, error) {
		var raw bytes.Buffer
		err := invoke([]string{"--json", "status"}, &raw)
		var report management.Report
		if err == nil {
			err = json.Unmarshal(raw.Bytes(), &report)
		}
		return report, err
	}
	changed := false
	var initial map[tools.ID]tools.State
	cursor := publicToolNames[0]
	for {
		if !host.explicit {
			var err error
			host.selected, err = host.frontend.ExecutionSelection()
			if err != nil {
				return err
			}
		}
		report, err := load()
		if err != nil {
			return err
		}
		if initial == nil {
			initial = report.Tools
		}
		choices := make([]huh.Option[string], 0, len(publicToolNames)+1)
		for _, name := range publicToolNames {
			state := report.Tools[tools.ID(name)]
			label := setupStateLabel(state)
			if state.Installed && state.Enabled {
				label += " | " + toolRuntimeLabel(state, report.Target.Mode)
			}
			if !toolSupportedOnHost(name, report.Target) {
				label = "requires a Linux host"
			}
			choices = append(choices, huh.NewOption(fmt.Sprintf("%s — %s — %s", name, label, setupToolDescriptions[name]), name))
		}
		choices = append(choices, huh.NewOption("Install multiple tools", "install-many"))
		if installed, _ := setupToolCounts(report); installed > 0 {
			choices = append(choices, huh.NewOption("Uninstall all tools (keep data)", "uninstall-all"))
		}
		if !host.explicit {
			choices = append(choices, huh.NewOption("Change execution host", "host"))
		}
		choices = append(choices, huh.NewOption("Done", ""))
		selected := cursor
		hostLabel := setupHostLabel(host.selected)
		form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Loki tools").Description("Host: " + hostLabel + "\nInstall, enable, disable or uninstall. ↑/↓ move · Enter choose · Esc back").Options(choices...).Value(&selected)))
		aborted, err := runSetupForm(ctx, form, input, terminal)
		if err != nil {
			return err
		}
		if aborted || selected == "" {
			return success(out, "Setup closed.", map[string]any{"changed": changed || !reflect.DeepEqual(initial, report.Tools)})
		}
		cursor = selected
		if selected == "uninstall-all" {
			confirmed := false
			form := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Uninstall all tools on " + hostLabel + "?").Description("Remove all tool programs and stop their services on this host. User data, credentials, the host and CLI are retained.").Affirmative("Uninstall all tools").Negative("Back").Value(&confirmed)))
			aborted, err := runSetupForm(ctx, form, input, terminal)
			if err != nil {
				return err
			}
			if !aborted && confirmed {
				setupWorking(terminal, "Uninstalling all tools", hostLabel)
				if err := invoke([]string{"tools", "uninstall", "--all"}, diagnostics); err != nil {
					if viewErr := showSetupResult(ctx, "Uninstall — needs attention", shortDiagnostic(err.Error()), err.Error(), input, terminal); viewErr != nil {
						return viewErr
					}
				} else {
					changed = true
					if err := showSetupResult(ctx, "Uninstall — completed", "All tool programs on "+hostLabel+" removed. User data and credentials are retained.", "", input, terminal); err != nil {
						return err
					}
				}
			}
			cursor = publicToolNames[0]
			continue
		}
		if selected == "install-many" {
			updated, err := installSetupTools(ctx, report, hostLabel, input, terminal, func(command []string) error { return invoke(command, diagnostics) })
			if err != nil {
				if viewErr := showSetupResult(ctx, "Installation — needs attention", shortDiagnostic(err.Error()), err.Error(), input, terminal); viewErr != nil {
					return viewErr
				}
			} else if updated {
				if err := showSetupResult(ctx, "Installation — completed", "The selected tools were installed on "+hostLabel+".\nReview installation and activation below; use Check readiness to verify runtime support.", "", input, terminal); err != nil {
					return err
				}
			}
			changed = changed || updated
			continue
		}
		if selected == "host" {
			_, updated, err := chooseSetupHost(ctx, host.frontend, input, diagnostics, false)
			if err != nil {
				if viewErr := showSetupResult(ctx, "Host selection — needs attention", shortDiagnostic(err.Error()), err.Error(), input, terminal); viewErr != nil {
					return viewErr
				}
			}
			changed = changed || updated
			continue
		}
		state := report.Tools[tools.ID(selected)]
		if !toolSupportedOnHost(selected, report.Target) {
			if err := showSetupResult(ctx, "Tool unavailable on this host", selected+" needs a Linux execution host. Choose Change execution host, then install it there.", "", input, terminal); err != nil {
				return err
			}
			continue
		}
		actions := setupToolActions(selected, state, report.Target.Mode)
		actionChoices := make([]huh.Option[string], 0, len(actions)+1)
		for _, action := range actions {
			actionChoices = append(actionChoices, huh.NewOption(action.label, action.name))
		}
		actionChoices = append(actionChoices, huh.NewOption("Back", ""))
		action := actions[0].name
		description := "Host: " + hostLabel + "\n" + setupStateLabel(state)
		if selected == "browser" && state.Installed {
			description += "\n" + browserFeatureMenuSummary(state)
		}
		form = huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(selected).Description(description).Options(actionChoices...).Value(&action)))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil {
			return err
		}
		if aborted || action == "" {
			continue
		}
		if action == "features" || action == "configure" && selected == "git" {
			var updated bool
			var actionErr error
			if action == "features" {
				updated, actionErr = setupBrowserFeatures(ctx, state, hostLabel, input, terminal, func(command []string) error { return invoke(command, diagnostics) })
			} else {
				updated, actionErr = setupGitSigning(ctx, hostLabel, input, terminal, invoke)
			}
			changed = changed || updated
			if actionErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := showSetupResult(ctx, selected+" configuration — needs attention", shortDiagnostic(actionErr.Error()), actionErr.Error(), input, terminal); err != nil {
					return err
				}
			}
			continue
		}
		if action == "uninstall" {
			confirmed := false
			form = huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Uninstall " + selected + "?").Description("Host: " + hostLabel + "\nOnly this tool's programs are removed. User data is retained. Full services may restart.").Affirmative("Uninstall").Negative("Back").Value(&confirmed)))
			aborted, err = runSetupForm(ctx, form, input, terminal)
			if err != nil {
				return err
			}
			if aborted || !confirmed {
				continue
			}
		}
		setupWorking(terminal, selected+": "+action, hostLabel)
		apply := func(command []string) error { return invoke(command, diagnostics) }
		if err := applySetupAction(selected, action, report, apply); err != nil {
			// Re-read state on the next pass, including a partially completed
			// installation or an activation whose service start failed.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if viewErr := showSetupResult(ctx, selected+" — needs attention", shortDiagnostic(err.Error())+"\nSaved changes are retained. Review the tool state and retry.", err.Error(), input, terminal); viewErr != nil {
				return viewErr
			}
		} else {
			changed = true
			if err := showSetupResult(ctx, selected+" — action completed", "Action: "+action+"\nHost: "+hostLabel+"\nReview the updated tool state in the menu.", "", input, terminal); err != nil {
				return err
			}
		}
	}
}

func applySetupAction(id, action string, report management.Report, invoke func([]string) error) error {
	command := func(args ...string) error { return invoke(args) }
	if action == "install-enable" {
		return command("setup", id)
	}
	if action == "configure" {
		return command("integrations", "setup", id)
	}
	if action == "enable" && id == "github" && report.Target.Mode == tools.Full {
		if err := command("tools", "enable", id, "--no-start"); err != nil {
			return err
		}
		return command("integrations", "setup", "github")
	}
	return command("tools", action, id)
}
