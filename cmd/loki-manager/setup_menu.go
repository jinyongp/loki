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
		return "not installed | —"
	}
	if state.Enabled {
		return "installed | enabled"
	}
	return "installed | disabled"
}

// Use the command router so the menu reads and changes the selected execution
// host, including protected Linux hosts and remembered Windows WSL hosts.
func runSetupMenu(ctx context.Context, prefix []string, input *os.File, out, diagnostics io.Writer) error {
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
		report, err := load()
		if err != nil {
			return err
		}
		if initial == nil {
			initial = report.Tools
		}
		choices := make([]huh.Option[string], 0, len(publicToolNames)+1)
		for _, name := range publicToolNames {
			choices = append(choices, huh.NewOption(fmt.Sprintf("%-13s %-24s %s", name, setupStateLabel(report.Tools[tools.ID(name)]), setupToolDescriptions[name]), name))
		}
		choices = append(choices, huh.NewOption("Done", ""))
		selected := cursor
		form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Loki setup").Description("Choose a tool to manage. ↑/↓ move · Enter choose · Esc exit").Options(choices...).Value(&selected)))
		aborted, err := runSetupForm(ctx, form, input, terminal)
		if err != nil {
			return err
		}
		if aborted || selected == "" {
			return success(out, "Setup closed.", map[string]any{"changed": changed || !reflect.DeepEqual(initial, report.Tools)})
		}
		cursor = selected
		state := report.Tools[tools.ID(selected)]
		actions := setupActions(state)
		actionChoices := make([]huh.Option[string], 0, len(actions)+1)
		for _, action := range actions {
			actionChoices = append(actionChoices, huh.NewOption(action.label, action.name))
		}
		actionChoices = append(actionChoices, huh.NewOption("Back", ""))
		action := actions[0].name
		form = huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(selected).Description(setupStateLabel(state)).Options(actionChoices...).Value(&action)))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil {
			return err
		}
		if aborted || action == "" {
			continue
		}
		if action == "uninstall" {
			confirmed := false
			form = huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Uninstall " + selected + "?").Description("Tool programs are removed. User data is retained. Full services may restart.").Affirmative("Uninstall").Negative("Back").Value(&confirmed)))
			aborted, err = runSetupForm(ctx, form, input, terminal)
			if err != nil {
				return err
			}
			if aborted || !confirmed {
				continue
			}
		}
		fmt.Fprintf(diagnostics, "%s: %s...\n", selected, action)
		apply := func(command []string) error { return invoke(command, diagnostics) }
		if err := applySetupAction(selected, action, report, apply, load); err != nil {
			// Re-read state on the next pass, including a partially completed
			// installation or an activation whose service start failed.
			fmt.Fprintf(diagnostics, "Could not finish %s: %v\nInspect the current state below and retry.\n", selected, err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else {
			changed = true
		}
	}
}

func applySetupAction(id, action string, report management.Report, invoke func([]string) error, load func() (management.Report, error)) error {
	command := func(args ...string) error { return invoke(args) }
	if strings.HasPrefix(action, "install") {
		if err := command("tools", "install", id); err != nil {
			return err
		}
		if action == "install" {
			return nil
		}
		action = "enable"
	}
	if action == "uninstall" && report.Target.Mode == tools.Full {
		if err := command("tools", "stop"); err != nil {
			return err
		}
	}
	if err := command("tools", action, id); err != nil {
		return err
	}
	updated, err := load()
	if err != nil {
		return err
	}
	if updated.Target.Mode != tools.Full {
		return nil
	}
	if action == "enable" && id == "github" {
		return command("integrations", "setup", "github")
	}
	for _, state := range updated.Tools {
		if state.Installed && state.Enabled {
			return command("tools", "start")
		}
	}
	return command("tools", "stop")
}
