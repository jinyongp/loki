package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
		if err := form.RunWithContext(ctx); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return nil, nil
			}
			return nil, err
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
