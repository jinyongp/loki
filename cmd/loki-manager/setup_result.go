package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"loki/internal/progress"
	"loki/internal/tools"
	"os"
	"regexp"
	"strings"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

var missingSharedLibrary = regexp.MustCompile(`error while loading shared libraries: ([A-Za-z0-9_.+-]+):`)

// Presentation summaries keep the original diagnostics available to JSON and
// the setup details screen. A wrapped launch failure can hide its useful cause.
func shortDiagnostic(message string) string {
	message = ansi.Strip(message)
	if match := missingSharedLibrary.FindStringSubmatch(message); len(match) == 2 {
		return "Missing Linux library: " + match[1] + ". Run loki setup on this execution host to repair browser dependencies, then check readiness again."
	}
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return ansi.Truncate(line, 220, "…")
}

func setupWorking(terminal *os.File, title, host string) {
	fmt.Fprintf(terminal, "\n[WORKING] %s\n  Host: %s\n  Progress output follows.\n\n", title, host)
}

func setupReadiness(ctx context.Context, host setupMenuHost, input, terminal *os.File) error {
	setupWorking(terminal, "Checking readiness", setupHostLabel(host.selected))
	var raw, diagnostics bytes.Buffer
	stop := progress.StartHeartbeat(ctx, progress.NewLineReporter(terminal), progress.HeartbeatOptions{Operation: "setup", Phase: "readiness", Message: "Still checking tool readiness"})
	checkErr := operatorStatus(ctx, host.frontend, host.selected, &commandOutput{Writer: &raw, json: true}, &diagnostics, true)
	stop()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var report operatorReport
	if err := json.Unmarshal(raw.Bytes(), &report); err != nil {
		if checkErr == nil {
			checkErr = err
		}
		return showSetupResult(ctx, "Readiness — could not finish", shortDiagnostic(checkErr.Error()), diagnostics.String()+"\n"+raw.String(), input, terminal)
	}
	title := "Readiness — ready"
	if checkErr != nil || report.Ready == nil || !*report.Ready {
		title = "Readiness — needs attention"
	}
	summary := "Host: " + setupHostLabel(host.selected)
	var details strings.Builder
	seen := map[string]bool{}
	for _, name := range publicToolNames {
		state, ok := report.Tools[tools.ID(name)]
		if !ok || !state.Installed {
			continue
		}
		summary += "\n" + name + ": " + toolRuntimeLabel(state, report.Target.Mode)
		if state.Reason != "" {
			summary += "\n" + shortDiagnostic(state.Reason)
			fmt.Fprintf(&details, "%s:\n%s\n\n", name, state.Reason)
			seen[name+": "+state.Reason] = true
		}
	}
	for _, issue := range report.Issues {
		if !seen[issue] {
			summary += "\n" + shortDiagnostic(issue)
			fmt.Fprintln(&details, issue)
		}
	}
	details.WriteString(diagnostics.String())
	return showSetupResult(ctx, title, summary, details.String(), input, terminal)
}

func showSetupResult(ctx context.Context, title, summary, details string, input, terminal *os.File) error {
	width, height, err := term.GetSize(int(terminal.Fd()))
	if err != nil {
		width, height = 80, 24
	}
	lines := strings.Split(ansi.Hardwrap(ansi.Strip(summary), max(20, width-8), false), "\n")
	limit := max(3, height-15)
	if len(lines) > limit {
		details = summary + "\n\n" + details
		summary = strings.Join(lines[:limit], "\n") + "\nMore output is available in Details."
	}
	for {
		choice := "back"
		options := []huh.Option[string]{huh.NewOption("Continue — return to menu", "back")}
		if strings.TrimSpace(details) != "" {
			options = append(options, huh.NewOption("Details — read diagnostic output", "details"))
		}
		form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(title).Description(summary).Options(options...).Value(&choice)))
		aborted, err := runSetupScreen(ctx, form, input, terminal, "RESULT · Review before continuing")
		if err != nil || aborted || choice != "details" {
			return err
		}
		if err := showSetupDetails(ctx, title, details, input, terminal); err != nil {
			return err
		}
	}
}

func showSetupDetails(ctx context.Context, title, details string, input, terminal *os.File) error {
	width, height, err := term.GetSize(int(terminal.Fd()))
	if err != nil {
		width, height = 80, 24
	}
	lines := strings.Split(ansi.Hardwrap(ansi.Strip(details), max(20, width-8), false), "\n")
	size := max(3, height-15)
	pages := (len(lines) + size - 1) / size
	page := 0
	for {
		choice := "back"
		options := []huh.Option[string]{huh.NewOption("Back to result", "back")}
		if page+1 < pages {
			options = append(options, huh.NewOption("Next page", "next"))
		}
		if page > 0 {
			options = append(options, huh.NewOption("Previous page", "previous"))
		}
		body := strings.Join(lines[page*size:min(len(lines), (page+1)*size)], "\n")
		form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(fmt.Sprintf("%s · Page %d/%d", title, page+1, pages)).Description(body).Options(options...).Value(&choice)))
		aborted, err := runSetupScreen(ctx, form, input, terminal, "DETAILS · Read only")
		if err != nil || aborted || choice == "back" {
			return err
		}
		if choice == "next" {
			page++
		} else {
			page--
		}
	}
}
