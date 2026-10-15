package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
)

// Keep the upstream keyboard and selection behavior, while fitting the field
// to the terminal and making options beyond the viewport discoverable.
type browserFeaturePicker struct {
	*huh.MultiSelect[string]
	features      []browserFeature
	host          string
	width, height int
	optionLines   int
}

func newBrowserFeaturePicker(features []browserFeature, host string, selected *[]string) *browserFeaturePicker {
	p := &browserFeaturePicker{
		MultiSelect: huh.NewMultiSelect[string]().Filterable(false),
		features:    features,
		host:        host,
	}
	p.MultiSelect.WithKeyMap(huh.NewDefaultKeyMap())
	p.WithWidth(76)
	p.Value(selected)
	return p
}

func (p *browserFeaturePicker) position() int {
	id, _ := p.Hovered()
	for i, feature := range p.features {
		if feature.id == id {
			return i
		}
	}
	return 0
}

func (p *browserFeaturePicker) WithWidth(width int) huh.Field {
	position := p.position()
	p.width = width
	options := make([]huh.Option[string], 0, len(p.features))
	p.optionLines = 0
	for _, feature := range p.features {
		label := feature.label
		if label != feature.id {
			label += " [" + feature.id + "]"
		}
		label = ansi.Wordwrap(label, max(8, width-8), "")
		label = ansi.Hardwrap(label, max(8, width-8), false)
		p.optionLines += strings.Count(label, "\n") + 1
		options = append(options, huh.NewOption(label, feature.id))
	}
	p.MultiSelect.WithWidth(width)
	p.Options(options...)
	// Updating option text restores the first selected item upstream. Keep the
	// user's focus when a resize changes wrapping instead.
	p.MultiSelect.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	for range position {
		p.MultiSelect.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	// Group.WithHeight only shrinks fields. Restore the full content height
	// before the form applies its new bounds, so a taller terminal expands too.
	p.height = 0
	p.refresh()
	return p
}

func (p *browserFeaturePicker) WithHeight(height int) huh.Field {
	p.height = height
	p.refresh()
	return p
}

func (p *browserFeaturePicker) refresh() {
	position := p.position()
	p.Title(fmt.Sprintf("Optional browser features (%d)", len(p.features)))
	description := "Host: " + p.host + "\n" + p.features[position].description
	status := fmt.Sprintf("Feature %d/%d", position+1, len(p.features))
	// Reserve a status line at every size; it never displaces an option when
	// moving between a fully visible list and a scrolling one.
	lines := func(text string) int {
		return strings.Count(ansi.Hardwrap(text, max(8, p.width-4), false), "\n") + 1
	}
	naturalHeight := lines(fmt.Sprintf("Optional browser features (%d)", len(p.features))) + lines(description) + 1 + p.optionLines
	if p.height > 0 && naturalHeight > p.height {
		status += " · ↑/↓ scroll for more"
	}
	naturalHeight += lines(status) - 1
	p.Description(description + "\n" + status)
	height := naturalHeight
	if p.height > 0 {
		height = min(height, p.height)
	}
	p.MultiSelect.Height(height)
}

func (p *browserFeaturePicker) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	_, cmd := p.MultiSelect.Update(msg)
	p.refresh()
	return p, cmd
}
