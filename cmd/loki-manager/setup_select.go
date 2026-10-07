package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"golang.org/x/term"
)

var setupToolDescriptions = map[string]string{
	"browser":      "Web navigation and screenshots",
	"workspace":    "Managed files and workspaces",
	"execution":    "Confined jobs and commands",
	"git":          "Git operations and optional signing",
	"github":       "GitHub repositories, issues and Projects",
	"secrets":      "Protected application secrets",
	"sharing":      "Managed published endpoints",
	"coordination": "External task coordination",
}

func selectSetupTools(ctx context.Context, input io.Reader, diagnostics io.Writer) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	in, inputFile := input.(*os.File)
	out, outputFile := diagnostics.(*os.File)
	if inputFile && outputFile && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd())) && os.Getenv("TERM") != "dumb" {
		var selected []string
		options := make([]huh.Option[string], 0, len(publicToolNames))
		for _, name := range publicToolNames {
			options = append(options, huh.NewOption(fmt.Sprintf("%-13s %s", name, setupToolDescriptions[name]), name))
		}
		keys := huh.NewDefaultKeyMap()
		keys.Quit.SetKeys("ctrl+c", "esc")
		keys.MultiSelect.Toggle.SetHelp("space", "select")
		form := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().
			Title("Loki setup").
			Description("Choose tools: ↑/↓ move · Space select · Enter confirm · Esc/Ctrl+C cancel").
			Options(options...).
			Filterable(false).
			Height(len(options) + 4).
			Value(&selected))).
			WithInput(input).
			WithOutput(diagnostics).
			WithKeyMap(keys).
			WithShowHelp(true)
		// Graceful quit waits for terminal input readers. Huh's default abort
		// interrupts the program and can close its input before readers stop.
		form.SubmitCmd, form.CancelCmd = tea.Quit, tea.Quit
		promptCtx, stop := context.WithCancel(ctx)
		defer stop()
		model := &setupSelectionModel{form: form, ctx: promptCtx}
		terminalOutput := setupTerminalOutput{File: out, fd: out.Fd()}
		if _, err := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(terminalOutput)).Run(); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if form.State == huh.StateAborted {
			return nil, nil
		}
		return selected, nil
	}

	// Pipes and limited terminals keep line input without consuming the next
	// wizard's input. The animated form never writes to JSON stdout.
	fmt.Fprintln(diagnostics, "Loki setup — choose the tools you need.")
	for _, name := range publicToolNames {
		fmt.Fprintf(diagnostics, "  %-13s %s\n", name, setupToolDescriptions[name])
	}
	fmt.Fprint(diagnostics, "Tools (space or comma separated; Enter cancels): ")
	line, err := readSetupLine(input, 4096)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(line) > 4096 {
		return nil, fmt.Errorf("tool selection exceeds input limit")
	}
	return strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n' }), nil
}

type setupSelectionCancelled struct{}

// Resize probes use a borrowed descriptor, without touching os.File's mutable
// close state after the prompt returns to its owner.
type setupTerminalOutput struct {
	*os.File
	fd uintptr
}

func (f setupTerminalOutput) Fd() uintptr { return f.fd }

// Adapt the form to Bubble Tea's view model and turn context cancellation into
// the same graceful exit as keyboard cancellation.
type setupSelectionModel struct {
	form *huh.Form
	ctx  context.Context
}

func (m *setupSelectionModel) Init() tea.Cmd {
	return tea.Batch(m.form.Init(), func() tea.Msg {
		<-m.ctx.Done()
		return setupSelectionCancelled{}
	})
}

func (m *setupSelectionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, cancelled := msg.(setupSelectionCancelled); cancelled {
		return m, tea.Quit
	}
	_, cmd := m.form.Update(msg)
	return m, cmd
}

func (m *setupSelectionModel) View() tea.View {
	v := tea.NewView(m.form.View())
	v.ReportFocus = true
	return v
}
