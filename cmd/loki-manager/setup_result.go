package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type setupSessionKey struct{}
type setupSessionResult struct{ title, summary string }
type setupSession struct {
	results         []setupSessionResult
	input, terminal *os.File
	cancel          context.CancelFunc
	screen          *setupScreen
}

func (s *setupSession) add(title, summary string) {
	for _, r := range s.results {
		if r.title == title && r.summary == summary {
			return
		}
	}
	s.results = append(s.results, setupSessionResult{title, summary})
}

func (s *setupSession) print(terminal *os.File) {
	if s.screen != nil {
		s.screen.close()
		s.screen = nil
	}
	if s.cancel != nil {
		s.cancel()
	}

	if len(s.results) == 1 {
		printSetupResult(terminal, s.results[0].title, s.results[0].summary)
	}
	if len(s.results) > 1 {
		fmt.Fprintln(terminal, "\n[RESULT] Setup summary")
		for _, r := range s.results {
			fmt.Fprintln(terminal, "  "+ansi.Strip(r.title))
			for _, line := range strings.Split(ansi.Strip(r.summary), "\n") {
				fmt.Fprintln(terminal, "    "+line)
			}
		}
		fmt.Fprintln(terminal)
	}
	s.results = nil
}

func printSetupResult(terminal *os.File, title, summary string) {
	fmt.Fprintf(terminal, "\n[RESULT] %s\n", ansi.Strip(title))
	for _, line := range strings.Split(ansi.Strip(summary), "\n") {
		fmt.Fprintf(terminal, "  %s\n", line)
	}
	fmt.Fprintln(terminal)
}

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

type setupActionError struct {
	err     error
	details string
	host    string
}

func (e *setupActionError) Error() string { return e.err.Error() }
func (e *setupActionError) Unwrap() error { return e.err }

func setupErrorSummary(err error) string {
	var action *setupActionError
	if errors.As(err, &action) && action.host != "" {
		return "Host: " + action.host + "\n" + shortDiagnostic(err.Error())
	}
	return shortDiagnostic(err.Error())
}

func setupErrorDetails(err error) string {
	var action *setupActionError
	if errors.As(err, &action) && action.details != "" {
		header := err.Error()
		if action.host != "" {
			header = "Host: " + action.host + "\n" + header
		}
		return header + "\n\n" + action.details
	}
	return err.Error()
}

func setupWorking(ctx context.Context, terminal *os.File, title, host string, action func(io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session, _ := ctx.Value(setupSessionKey{}).(*setupSession); session != nil {
		screen := session.startScreen(ctx)
		if err := screen.showWork(ctx, title, host); err != nil {
			return err
		}
		log := &setupScreenLog{screen: screen, title: title, host: host}
		if err := action(log); err != nil {
			return &setupActionError{err: err, details: log.tail.String(), host: host}
		}
		return nil
	}
	restore, err := setupProgressOutput(terminal)
	if err != nil {
		return err
	}
	defer restore()
	// Keep streamed logs on the temporary screen. Restore scrollback before a
	// result or another form is shown, including on failure or cancellation.
	fmt.Fprint(terminal, ansi.SetModeAltScreenSaveCursor+ansi.EraseEntireScreen+ansi.CursorHomePosition)
	defer fmt.Fprint(terminal, ansi.ResetModeAltScreenSaveCursor)
	fmt.Fprintf(terminal, "[WORKING] %s\r\n  Host: %s\r\n\r\n", title, host)
	var captured diagnosticTail
	if err := action(io.MultiWriter(terminal, &captured)); err != nil {
		return &setupActionError{err: err, details: captured.String(), host: host}
	}
	return nil
}

func setupReadiness(ctx context.Context, host setupMenuHost, input, terminal *os.File) error {
	var raw bytes.Buffer
	var diagnostics diagnosticTail
	checkErr := setupWorking(ctx, terminal, "Checking readiness", setupHostLabel(host.selected), func(log io.Writer) error {
		stop := progress.StartHeartbeat(ctx, progress.NewLineReporter(log), progress.HeartbeatOptions{Operation: "setup", Phase: "readiness", Message: "Still checking tool readiness"})
		defer stop()
		return operatorStatus(ctx, host.frontend, host.selected, &commandOutput{Writer: &raw, json: true}, io.MultiWriter(log, &diagnostics), true)
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var report operatorReport
	if err := json.Unmarshal(raw.Bytes(), &report); err != nil {
		if checkErr == nil {
			checkErr = err
		}
		return showSetupResult(ctx, "Readiness — could not finish", "Host: "+setupHostLabel(host.selected)+"\n"+shortDiagnostic(checkErr.Error()), setupErrorDetails(checkErr)+"\n"+raw.String(), input, terminal)
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
	summary += "\nCodex connection not tested."
	return showSetupResult(ctx, title, summary, details.String(), input, terminal)
}

func showSetupResult(ctx context.Context, title, summary, details string, input, terminal *os.File) error {
	ctx, ownSession := setupSessionContext(ctx, input, terminal)
	if ownSession != nil {
		defer ownSession.print(terminal)
	}
	// Result screens use the alternate buffer. Preserve the untruncated summary
	// after that buffer is restored, including when the user exits with Escape.
	persistentSummary := ansi.Strip(summary)
	defer ctx.Value(setupSessionKey{}).(*setupSession).add(title, persistentSummary)
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
			options = append(options, huh.NewOption("Details — read full output", "details"))
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
