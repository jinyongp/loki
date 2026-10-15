package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"

	"loki/internal/management"
	"loki/internal/tools"
)

type setupToolChoice struct {
	id, label, detail string
	installable       bool
}

// One list serves both individual management and batch installation. Space
// marks missing tools; Enter still opens the usual actions when none are marked.
type setupToolsPicker struct {
	*huh.Select[string]
	choices       []setupToolChoice
	marked        map[string]bool
	cursor        *string
	host          string
	width, height int
}

func newSetupToolsPicker(report management.Report, host string, changeHost bool, cursor *string) *setupToolsPicker {
	p := &setupToolsPicker{
		Select: huh.NewSelect[string]().Value(cursor),
		marked: make(map[string]bool), host: host, cursor: cursor,
	}
	keys := huh.NewDefaultKeyMap()
	keys.Select.Filter.SetEnabled(false)
	p.Select.WithKeyMap(keys)
	nameWidth := 0
	for _, name := range publicToolNames {
		nameWidth = max(nameWidth, len(name))
	}
	for _, name := range publicToolNames {
		state := report.Tools[tools.ID(name)]
		status, detail := "not installed", setupStateLabel(state)
		if state.Installed {
			status = "disabled"
			if state.Enabled {
				status = "enabled"
				detail += " | " + toolRuntimeLabel(state, report.Target.Mode)
			}
		}
		supported := toolSupportedOnHost(name, report.Target)
		if !supported {
			status, detail = "unavailable", "Requires a Linux execution host."
		}
		p.choices = append(p.choices, setupToolChoice{
			id: name, label: fmt.Sprintf("%-*s  %s", nameWidth, name, status),
			detail:      name + " — " + setupToolDescriptions[name] + "\n" + detail,
			installable: supported && !state.Installed,
		})
	}
	if installed, _ := setupToolCounts(report); installed > 0 {
		p.choices = append(p.choices, setupToolChoice{id: "uninstall-all", label: "Uninstall all tools (keep data)", detail: "Remove all tool programs. User data and credentials are retained."})
	}
	if changeHost {
		p.choices = append(p.choices, setupToolChoice{id: "host", label: "Change execution host", detail: "Choose where Loki tools are installed and run."})
	}
	p.choices = append(p.choices, setupToolChoice{id: "", label: "Done", detail: "Return to setup. Completed actions are retained."})
	p.WithWidth(76)
	return p
}

func (p *setupToolsPicker) installations() []string {
	var selected []string
	for _, choice := range p.choices {
		if p.marked[choice.id] {
			selected = append(selected, choice.id)
		}
	}
	return selected
}

func (p *setupToolsPicker) rebuildOptions() {
	options := make([]huh.Option[string], 0, len(p.choices))
	for _, choice := range p.choices {
		marker := "    "
		if choice.installable {
			marker = "[ ] "
			if p.marked[choice.id] {
				marker = "[x] "
			}
		}
		options = append(options, huh.NewOption(ansi.Truncate(marker+choice.label, max(8, p.width-4), "…"), choice.id))
	}
	p.Options(options...)
}

func (p *setupToolsPicker) WithWidth(width int) huh.Field {
	p.width = width
	p.Select.WithWidth(width)
	p.rebuildOptions()
	// Restore natural height before Group shrinks the field for the new size.
	p.height = 0
	p.refresh()
	return p
}

func (p *setupToolsPicker) WithHeight(height int) huh.Field {
	p.height = height
	p.refresh()
	return p
}

func (p *setupToolsPicker) refresh() {
	id, _ := p.Hovered()
	position, detail := 0, ""
	for i, choice := range p.choices {
		if choice.id == id {
			position, detail = i, choice.detail
			break
		}
	}
	status := "Space select to install · Enter manage"
	if count := len(p.installations()); count > 0 {
		status = fmt.Sprintf("%d selected · Space toggle · Enter install", count)
	}
	wrap := func(text string) string {
		return ansi.Hardwrap(ansi.Wordwrap(text, max(8, p.width-4), ""), max(8, p.width-4), false)
	}
	// Keep the detail area at a constant height as focus moves between tools
	// and menu actions, so the rows do not jump up and down.
	detail = wrap(detail)
	detailLines := 2
	for _, choice := range p.choices {
		detailLines = max(detailLines, strings.Count(wrap(choice.detail), "\n")+1)
	}
	detail += strings.Repeat("\n", detailLines-strings.Count(detail, "\n")-1)
	description := wrap("Host: "+p.host) + "\n" + detail + "\n" + wrap(status)
	lines := func(text string) int { return strings.Count(text, "\n") + 1 }
	naturalHeight := 1 + lines(description) + 1 + len(p.choices)
	positionText := fmt.Sprintf("Item %d/%d", position+1, len(p.choices))
	if p.height > 0 && naturalHeight > p.height {
		positionText += " · ↑/↓ scroll for more"
	}
	description += "\n" + wrap(positionText)
	naturalHeight = 1 + lines(description) + len(p.choices)
	p.Title("Loki tools").Description(description)
	if p.height > 0 {
		naturalHeight = min(naturalHeight, p.height)
	}
	p.Select.Height(naturalHeight)
}

func (p *setupToolsPicker) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.Code == tea.KeySpace {
		id, _ := p.Hovered()
		for _, choice := range p.choices {
			if choice.id == id && choice.installable {
				p.marked[id] = !p.marked[id]
				p.rebuildOptions()
				break
			}
		}
		p.refresh()
		return p, nil
	}
	_, cmd := p.Select.Update(msg)
	// Upstream End navigation moves the hover without updating the accessor.
	// Keep it in sync before resizing rebuilds the option labels.
	if id, ok := p.Hovered(); ok {
		*p.cursor = id
	}
	p.refresh()
	return p, cmd
}
