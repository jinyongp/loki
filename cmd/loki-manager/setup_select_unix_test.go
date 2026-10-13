//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"loki/internal/management"
	"loki/internal/tools"
)

func TestSetupMenuKeyboardBackAndExit(t *testing.T) {
	for _, kind := range []string{"tools", "home", "fresh", "unreachable", "preparation-failed", "connections", "result", "result-narrow", "readiness"} {
		t.Run(kind, func(t *testing.T) { testSetupMenuKeyboardBackAndExit(t, kind) })
	}
}

func testSetupMenuKeyboardBackAndExit(t *testing.T, kind string) {
	t.Setenv("TERM", "xterm-256color")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer terminal.Close()
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 30, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	if kind == "result-narrow" {
		if err := pty.Setsize(terminal, &pty.Winsize{Rows: 20, Cols: 50}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			select {
			case chunks <- string(buffer[:n]):
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	waitFor := func(text string) {
		t.Helper()
		var screen strings.Builder
		for !strings.Contains(screen.String(), text) {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatal("terminal closed before rendering", text)
				}
				screen.WriteString(chunk)
			case <-ctx.Done():
				t.Fatal("menu did not render", text)
			}
		}
	}
	root := filepath.Join(t.TempDir(), "untouched")
	if kind == "connections" || kind == "readiness" {
		t.Setenv("CODEX_HOME", t.TempDir())
	}
	if kind == "readiness" {
		installSetupSSHFixture(t, `{"target":{"os":"linux","arch":"amd64","mode":"project-host"},"ready":false,"healthy":false,"tools":{"browser":{"installed":true,"enabled":true,"readiness":"degraded","reason":"Chrome startup failed\n<launching> long Chrome command\nerror while loading shared libraries: libnspr4.so: cannot open shared object file"}}}`)
	}
	if kind == "unreachable" || kind == "preparation-failed" {
		installSetupSSHFixture(t, `{}`)
		t.Setenv("CODEX_HOME", t.TempDir())
		if kind == "preparation-failed" {
			t.Setenv("LOKI_REVIEW_SSH_UNREACHABLE", "1")
		}
	}
	var output bytes.Buffer
	finished := make(chan error, 1)
	go func() {
		host := setupMenuHost{frontend: management.Store{Root: root}}
		if kind == "readiness" {
			host.selected = &management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "loki"}
			err := setupReadiness(ctx, host, terminal, terminal)
			if err == nil {
				err = success(&commandOutput{Writer: &output, json: true}, "", map[string]any{"changed": false})
			}
			finished <- err
		} else if strings.HasPrefix(kind, "result") {
			err := showSetupResult(ctx, "Readiness — needs attention", "Missing Linux library: libnspr4.so", strings.Repeat("Chrome fixture diagnostic line\n", 40), terminal, terminal)
			if err == nil {
				err = success(&commandOutput{Writer: &output, json: true}, "", map[string]any{"changed": false})
			}
			finished <- err
		} else if kind == "connections" {
			report := management.Report{Target: management.LocalTarget(tools.ProjectHost), Tools: map[tools.ID]tools.State{"browser": {Installed: true, Enabled: true}}}
			changed, err := setupConnections(ctx, host, report, terminal, terminal)
			if err == nil {
				err = success(&commandOutput{Writer: &output, json: true}, "", map[string]any{"changed": changed})
			}
			finished <- err
		} else if kind == "tools" {
			finished <- runToolsMenu(ctx, []string{"--root", root}, host, terminal, &commandOutput{Writer: &output, json: true}, terminal)
		} else {
			host.explicit = kind == "home"
			if kind == "unreachable" || kind == "preparation-failed" {
				host.explicit = true
				host.selected = &management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "loki"}
			}
			finished <- runSetupMenu(ctx, []string{"--root", root}, host, terminal, &commandOutput{Writer: &output, json: true}, terminal)
		}
	}()
	if kind == "readiness" {
		waitFor("Missing Linux library")
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("<launching>")
		_, _ = io.WriteString(master, "\x1b")
		waitFor("Missing Linux library")
		_, _ = io.WriteString(master, "\r")
	} else if strings.HasPrefix(kind, "result") {
		waitFor("Missing Linux library")
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("Page 1/")
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("Page 2/")
		_, _ = io.WriteString(master, "\x1b")
		waitFor("Missing Linux library")
		_, _ = io.WriteString(master, "\r")
	} else if kind == "connections" {
		waitFor("Connect Codex")
		_, _ = io.WriteString(master, "\r")
		waitFor("Loki tools: browser")
		_, _ = io.WriteString(master, "\x1b")
	} else if kind == "fresh" {
		waitFor("Choose execution host")
		_, _ = io.WriteString(master, "\x1b")
	} else if kind == "unreachable" || kind == "preparation-failed" {
		if kind == "preparation-failed" {
			waitFor("Host preparation — needs attention")
			_, _ = io.WriteString(master, "\r")
		}
		waitFor("Host unavailable")
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("Inspect client connections")
		_, _ = io.WriteString(master, "\x1b[B\r")
		waitFor("codex: not configured")
		_, _ = io.WriteString(master, "\r")
		waitFor("Host unavailable")
		_, _ = io.WriteString(master, "\x1b")
	} else {
		if kind == "home" {
			waitFor("Manage tools")
			_, _ = io.WriteString(master, "\r")
		}
		waitFor("coordination")
		_, _ = io.WriteString(master, "\r")
		waitFor("Install only")
		_, _ = io.WriteString(master, "\x1b")
		waitFor("coordination")
		if kind == "tools" {
			_, _ = io.WriteString(master, strings.Repeat("\x1b[B", len(publicToolNames)+1)+"\r")
			waitFor("Choose execution host")
			_, _ = io.WriteString(master, "\x1b")
			waitFor("coordination")
		}
		_, _ = io.WriteString(master, "\x1b")
		if kind == "home" {
			waitFor("Manage tools")
			_, _ = io.WriteString(master, "\x1b")
		}
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("menu did not exit")
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("terminal was not restored", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("cancelled menu changed installation", err)
	}
	var result struct {
		Changed bool `json:"changed"`
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || !result.Success || result.Changed {
		t.Fatalf("menu contaminated JSON output: %s (%v)", output.String(), err)
	}
}

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
