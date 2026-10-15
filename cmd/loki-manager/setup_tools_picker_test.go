package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"

	"loki/internal/management"
	"loki/internal/tools"
)

func TestSetupToolsPickerAlignedRowsSelectionAndResize(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent=%t", persistent), func(t *testing.T) {
			report := management.Report{
				Target: tools.Target{OS: "linux", Arch: "amd64", Mode: tools.ProjectHost},
				Tools:  map[tools.ID]tools.State{"browser": {Installed: true, Enabled: true}},
			}
			cursor := "browser"
			picker := newSetupToolsPicker(report, "WSL: Ubuntu", true, &cursor)
			form := huh.NewForm(huh.NewGroup(picker))
			var model tea.Model = &setupSelectionModel{form: form, ctx: context.Background()}
			if persistent {
				model = &setupScreenModel{form: &setupFormRequest{form: form, phase: "CHOOSE"}}
			}
			picker.Focus()
			update := func(msg tea.Msg) string {
				model.Update(msg)
				return ansi.Strip(model.View().Content)
			}
			large := update(tea.WindowSizeMsg{Width: 120, Height: 30})
			t.Logf("Wide terminal:\n%s", large)
			if strings.Contains(large, "Install multiple tools") || strings.Contains(large, "scroll for more") {
				t.Fatal("wide list duplicates installation or hides rows", large)
			}
			if !strings.Contains(large, "Web navigation and screenshots") || strings.Contains(large, "Managed files and workspaces") {
				t.Fatal("tool descriptions are not limited to the focused tool", large)
			}
			stateColumn := -1
			for _, line := range strings.Split(large, "\n") {
				column := strings.Index(line, "not installed")
				if column >= 0 {
					if stateColumn >= 0 && stateColumn != column {
						t.Fatal("installation states are not aligned", large)
					}
					stateColumn = column
				}
			}
			if stateColumn < 0 {
				t.Fatal("missing installation states", large)
			}
			update(tea.KeyPressMsg{Code: tea.KeySpace}) // Installed tools cannot be marked.
			update(tea.KeyPressMsg{Code: tea.KeyDown})
			update(tea.KeyPressMsg{Code: tea.KeySpace})
			update(tea.KeyPressMsg{Code: tea.KeyDown})
			marked := update(tea.KeyPressMsg{Code: tea.KeySpace})
			if !slices.Equal(picker.installations(), []string{"workspace", "execution"}) || !strings.Contains(marked, "2 selected") || !strings.Contains(marked, "Enter install") {
				t.Fatal("list does not select multiple missing tools", picker.installations(), marked)
			}
			small := update(tea.WindowSizeMsg{Width: 50, Height: 20})
			t.Logf("Narrow terminal:\n%s", small)
			if !strings.Contains(small, "scroll for more") || strings.Count(small, "\n") >= 20 {
				t.Fatal("short terminal hides overflow or exceeds its height", small)
			}
			for _, line := range strings.Split(small, "\n") {
				if ansi.StringWidth(line) > 50 {
					t.Fatal("narrow terminal overflows horizontally", line)
				}
			}
			last := update(tea.KeyPressMsg{Code: tea.KeyEnd})
			if !strings.Contains(last, "Done") || !strings.Contains(last, "Item 11/11") {
				t.Fatal("last action is unreachable", last)
			}
			expanded := update(tea.WindowSizeMsg{Width: 140, Height: 45})
			if id, _ := picker.Hovered(); id != "" || !strings.Contains(expanded, "coordination") || strings.Contains(expanded, "scroll for more") || !slices.Equal(picker.installations(), []string{"workspace", "execution"}) {
				t.Fatal("resize lost focus, selections or rows", expanded)
			}
			update(tea.KeyPressMsg{Code: tea.KeyHome})
			update(tea.KeyPressMsg{Code: tea.KeyDown})
			update(tea.KeyPressMsg{Code: tea.KeySpace})
			if !slices.Equal(picker.installations(), []string{"execution"}) {
				t.Fatal("unmarking one tool changed another selection", picker.installations())
			}
		})
	}
}

func TestSetupToolsPickerRejectsUnsupportedInstallation(t *testing.T) {
	cursor := "workspace"
	picker := newSetupToolsPicker(management.Report{Target: tools.Target{OS: "darwin", Arch: "arm64"}}, "Local macOS", false, &cursor)
	picker.Focus()
	picker.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(picker.installations()) != 0 || !strings.Contains(ansi.Strip(picker.View()), "Requires a Linux execution host") {
		t.Fatal("unsupported tool can be marked for installation", picker.installations(), picker.View())
	}
}
