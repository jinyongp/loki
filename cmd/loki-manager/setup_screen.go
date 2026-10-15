package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// A setup session owns one renderer. Forms replace its model while actions send
// progress to it, so the terminal's normal buffer is restored only on exit.
type setupScreen struct {
	program *tea.Program
	done    chan struct{}
	err     error
}

type setupFormRequest struct {
	form     *huh.Form
	phase    string
	finished chan bool
}
type setupFormFinished struct{ form *huh.Form }
type setupWorkView struct {
	title, host, log string
	ready            chan struct{}
}

type setupScreenModel struct {
	form          *setupFormRequest
	work          *setupWorkView
	view          tea.View
	width, height int
	cancel        context.CancelFunc
}

func (m *setupScreenModel) Init() tea.Cmd { return nil }
func (m *setupScreenModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case setupFormRequest:
		m.form, m.work = &msg, nil
		if m.width > 0 {
			resizeSetupForm(msg.form, m.width, m.height)
		}
		return m, msg.form.Init()
	case setupFormFinished:
		if m.form != nil && m.form.form == msg.form {
			m.form.finished <- msg.form.State == huh.StateAborted
			m.form = nil
		}
		return m, nil
	case setupWorkView:
		m.work = &msg
		if msg.ready != nil {
			close(msg.ready)
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.form != nil {
			resizeSetupForm(m.form.form, msg.Width, msg.Height)
		}
	case tea.KeyPressMsg:
		if m.form == nil && msg.String() == "ctrl+c" {
			m.cancel()
			return m, tea.Quit
		}
	}
	if m.form != nil {
		_, cmd := m.form.form.Update(msg)
		return m, cmd
	}
	return m, nil
}
func (m *setupScreenModel) View() tea.View {
	if m.form != nil && m.form.form.State == huh.StateNormal {
		m.view = tea.NewView("Loki setup · " + m.form.phase + "\n\n" + m.form.form.View())
	} else if m.work != nil && m.form == nil {
		header := "[WORKING] " + m.work.title + "\n  Host: " + m.work.host + "\n\n"
		lines := strings.Split(ansi.Hardwrap(strings.ReplaceAll(m.work.log, "\r", ""), max(20, m.width), true), "\n")
		if limit := max(1, m.height-5); len(lines) > limit {
			lines = lines[len(lines)-limit:]
		}
		m.view = tea.NewView(header + strings.Join(lines, "\n"))
	}
	m.view.AltScreen = true
	m.view.ReportFocus = true
	return m.view
}

func setupSessionContext(ctx context.Context, input, terminal *os.File) (context.Context, *setupSession) {
	if _, ok := ctx.Value(setupSessionKey{}).(*setupSession); ok {
		return ctx, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	session := &setupSession{input: input, terminal: terminal, cancel: cancel}
	return context.WithValue(ctx, setupSessionKey{}, session), session
}

func (s *setupSession) startScreen(ctx context.Context) *setupScreen {
	if s.screen == nil {
		screen := &setupScreen{done: make(chan struct{})}
		model := &setupScreenModel{cancel: s.cancel}
		// Context cancellation kills Bubble Tea without waiting for its input
		// reader. Request a graceful quit instead, so terminal descriptors stay
		// valid until all readers have stopped and Run has restored the console.
		screen.program = tea.NewProgram(model, tea.WithInput(s.input), tea.WithOutput(setupTerminalOutput{File: s.terminal, fd: s.terminal.Fd()}))
		s.screen = screen
		go func() {
			_, screen.err = screen.program.Run()
			close(screen.done)
		}()
		go func() {
			select {
			case <-ctx.Done():
				screen.program.Quit()
			case <-screen.done:
			}
		}()
	}
	return s.screen
}

func (s *setupScreen) runForm(ctx context.Context, form *huh.Form, phase string) (bool, error) {
	finished := make(chan bool, 1)
	complete := func() tea.Msg { return setupFormFinished{form} }
	form.SubmitCmd, form.CancelCmd = complete, complete
	s.program.Send(setupFormRequest{form, phase, finished})
	select {
	case aborted := <-finished:
		return aborted, ctx.Err()
	case <-s.done:
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return false, s.err
	}
}

func (s *setupScreen) close() {
	s.program.Quit()
	<-s.done
}

func (s *setupScreen) showWork(ctx context.Context, title, host string) error {
	ready := make(chan struct{})
	s.program.Send(setupWorkView{title: title, host: host, ready: ready})
	select {
	case <-ready:
		return ctx.Err()
	case <-s.done:
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.err
	}
}

type setupScreenLog struct {
	screen      *setupScreen
	title, host string
	mu          sync.Mutex
	tail        diagnosticTail
}

func (l *setupScreenLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.tail.Write(p)
	l.screen.program.Send(setupWorkView{title: l.title, host: l.host, log: l.tail.String()})
	return n, err
}

// Keep input in the same reader as terminal capability replies. Giving stdin
// to a second reader can mix a late capability response into a password.
func setupPassword(ctx context.Context, input *os.File, diagnostics io.Writer, prompt string) ([]byte, error) {
	session, _ := ctx.Value(setupSessionKey{}).(*setupSession)
	if session == nil {
		fmt.Fprint(diagnostics, prompt)
		value, err := term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(diagnostics)
		return value, err
	}
	var value string
	field := huh.NewInput().Title(strings.TrimSpace(prompt)).Description("Input hidden. Enter confirms; Esc cancels.").EchoMode(huh.EchoModePassword).Value(&value)
	defer func() {
		value = ""
		field.Value(&value)
	}()
	aborted, err := runSetupScreen(ctx, huh.NewForm(huh.NewGroup(field)), input, session.terminal, "INPUT · Your input is needed")
	if err != nil {
		return nil, err
	}
	if aborted {
		return nil, context.Canceled
	}
	return []byte(value), nil
}

func setupTunnelID(ctx context.Context, input *os.File, diagnostics io.Writer) (string, error) {
	session, _ := ctx.Value(setupSessionKey{}).(*setupSession)
	if session == nil {
		fmt.Fprint(diagnostics, "OpenAI tunnel ID: ")
		return readSetupLine(input, 256)
	}
	var value string
	field := huh.NewInput().Title("OpenAI tunnel ID").Value(&value).Validate(func(value string) error {
		if len(value) > 256 {
			return fmt.Errorf("tunnel ID exceeds 256 bytes")
		}
		return nil
	})
	aborted, err := runSetupForm(ctx, huh.NewForm(huh.NewGroup(field)), input, session.terminal)
	if err != nil {
		return "", err
	}
	if aborted {
		return "", context.Canceled
	}
	return value, nil
}
