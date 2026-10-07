//go:build linux || darwin

package main

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"loki/internal/tools"
)

func TestSetupKeyboardSelection(t *testing.T) {
	for _, test := range []struct {
		name string
		keys string
		want []tools.ID
	}{
		{"multiple and toggle", "\x1b[B \x1b[B\x1b[B \x1b[A\x1b[A  \r", []tools.ID{"workspace", "git"}},
		{"select all", "\x01\r", []tools.ID{"browser", "workspace", "execution", "git", "github", "secrets", "sharing", "coordination"}},
		{"empty confirmation", "\r", nil},
		{"cancel checked choice", " \x03", nil},
		{"escape", " \x1b", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			options, err := setupKeyboardPrompt(t, test.keys, false)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(options.selected, test.want) {
				t.Fatalf("selected %v, want %v", options.selected, test.want)
			}
		})
	}
}

func TestSetupKeyboardContextCancellation(t *testing.T) {
	_, err := setupKeyboardPrompt(t, "", true)
	if err == nil {
		t.Fatal("context cancellation was ignored")
	}
}

func setupKeyboardPrompt(t *testing.T, keys string, cancelPrompt bool) (setupOptions, error) {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer terminal.Close()
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type result struct {
		options setupOptions
		err     error
	}
	finished := make(chan result, 1)
	go func() {
		options, err := parseSetup(ctx, nil, terminal, terminal)
		finished <- result{options, err}
	}()
	ready := make(chan struct{}, 1)
	go func() {
		var screen strings.Builder
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			screen.Write(buffer[:n])
			if strings.Contains(screen.String(), "coordination") {
				ready <- struct{}{}
				_, _ = io.Copy(io.Discard, master)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("checkbox list did not render all eight tools")
	}
	if cancelPrompt {
		cancel()
	} else if _, err := io.WriteString(master, keys); err != nil {
		t.Fatal(err)
	}
	var completed result
	select {
	case completed = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("keyboard confirmation/cancellation did not finish")
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal mode was not restored: %v", err)
	}
	return completed.options, completed.err
}
