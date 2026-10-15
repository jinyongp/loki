package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"

	"loki/internal/tools"
)

func TestBrowserFeaturePickerResizeKeepsAllOptionsReachable(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "session"}[persistent], func(t *testing.T) {
			selected := []string{"pdf"}
			picker := newBrowserFeaturePicker(browserFeatures, "WSL: Ubuntu", &selected)
			form := huh.NewForm(huh.NewGroup(picker))
			var model tea.Model = &setupSelectionModel{form: form, ctx: context.Background()}
			if persistent {
				model = &setupScreenModel{form: &setupFormRequest{form: form, phase: "CHOOSE"}}
			}
			picker.Focus()
			update := func(msg tea.Msg) string {
				t.Helper()
				model.Update(msg)
				return ansi.Strip(model.View().Content)
			}
			large := update(tea.WindowSizeMsg{Width: 140, Height: 45})
			if !strings.Contains(large, "[unsafe-code]") || strings.Contains(large, "scroll for more") {
				t.Fatal("tall terminal does not show the full feature list", large)
			}
			small := update(tea.WindowSizeMsg{Width: 50, Height: 20})
			t.Logf("Short terminal:\n%s", small)
			if !strings.Contains(small, "scroll for more") || strings.Contains(small, "[unsafe-code]") || strings.Count(small, "\n") >= 20 {
				t.Fatal("short terminal hides overflow or exceeds its height", small)
			}
			last := update(tea.KeyPressMsg{Code: tea.KeyEnd})
			if !strings.Contains(last, "[unsafe-code]") || !strings.Contains(last, "Feature 14/14") || !strings.Contains(last, "Allow code execution") {
				t.Fatal("last feature or its description is unreachable", last)
			}
			update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			expanded := update(tea.WindowSizeMsg{Width: 140, Height: 45})
			t.Logf("Expanded terminal:\n%s", expanded)
			if picker.position() != len(browserFeatures)-1 || !strings.Contains(expanded, "[devtools]") || !strings.Contains(expanded, "[unsafe-code]") || strings.Contains(expanded, "scroll for more") {
				t.Fatal("expanding failed to restore options or preserve focus", expanded)
			}
			if !slices.Equal(selected, []string{"pdf", "unsafe-code"}) {
				t.Fatal("resizing or navigation changed the selection", selected)
			}
		})
	}
}

func TestBrowserFeatureSummaryListsSelectionsOnSeparateLines(t *testing.T) {
	ids := make([]string, len(browserFeatures))
	for i, feature := range browserFeatures {
		ids[i] = feature.id
	}
	summary := browserFeatureSummary(tools.State{Capabilities: ids})
	if !strings.Contains(summary, "14 selected") {
		t.Fatal("summary omits selected count", summary)
	}
	for _, id := range ids {
		if !strings.Contains(summary, "\n  • "+browserFeatureLabel(id)+"\n") && !strings.HasSuffix(summary, "\n  • "+browserFeatureLabel(id)) {
			t.Fatal("feature is not on its own line", id, summary)
		}
	}
}
