package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/xpty"
	"golang.org/x/term"

	"loki/internal/management"
)

type setupConsoleResult struct {
	Mask     int    `json:"mask"`
	Restored bool   `json:"restored"`
	Error    string `json:"error,omitempty"`
}

// Run the real selector in a child console; the parent only owns its ConPTY.
func TestSetupConsoleHelper(t *testing.T) {
	if os.Getenv("LOKI_SETUP_CONSOLE_HELPER") != "1" {
		return
	}
	before, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	var options setupOptions
	if root := os.Getenv("LOKI_SETUP_MENU_ROOT"); root != "" {
		host := setupMenuHost{frontend: management.Store{Root: root}}
		if kind := os.Getenv("LOKI_SETUP_MENU_KIND"); strings.HasPrefix(kind, "working-") {
			setupProgressFixture(t, os.Stderr, kind)
		} else if kind == "home" || kind == "fresh" {
			host.explicit = kind == "home"
			err = runSetupMenu(t.Context(), []string{"--root", root}, host, os.Stdin, os.Stdout, os.Stderr)
		} else {
			err = runToolsMenu(t.Context(), []string{"--root", root}, host, os.Stdin, os.Stdout, os.Stderr)
		}
	} else {
		options, err = parseSetup(t.Context(), nil, os.Stdin, os.Stderr)
	}
	after, restoreErr := term.GetState(int(os.Stdin.Fd()))
	result := setupConsoleResult{Restored: restoreErr == nil && reflect.DeepEqual(before, after)}
	if err != nil {
		result.Error = err.Error()
	}
	for _, selected := range options.selected {
		for i, name := range publicToolNames {
			if string(selected) == name {
				result.Mask |= 1 << i
			}
		}
	}
	raw, _ := json.Marshal(result)
	fmt.Printf("\nLOKI_SETUP_RESULT:%s\n", raw)
	os.Exit(0)
}

func TestSetupWindowsKeyboardConsole(t *testing.T) {
	for _, test := range []struct {
		name string
		keys string
		mask int
		menu bool
	}{
		{"multiple", "\x1b[B \x1b[B\x1b[B \r", 10, false},
		{"select all", "\x01\r", 255, false},
		{"empty", "\r", 0, false},
		{"cancel checked choice", " \x03", 0, false},
		{"escape", " \x1b", 0, false},
		{"management menu exit", "\x1b", 0, true},
		{"home", "\x1b", 0, true},
		{"fresh", "\x1b", 0, true},
		{"working-success", "", 0, true},
		{"working-failure", "", 0, true},
		{"working-cancel", "", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			console, err := xpty.NewConPty(100, 30)
			if err != nil {
				t.Fatal(err)
			}
			defer console.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestSetupConsoleHelper$")
			for _, entry := range os.Environ() {
				upper := strings.ToUpper(entry)
				if !strings.HasPrefix(upper, "TERM=") && !strings.HasPrefix(upper, "LOKI_SETUP_CONSOLE_HELPER=") && !strings.HasPrefix(upper, "LOKI_SETUP_MENU_ROOT=") && !strings.HasPrefix(upper, "LOKI_SETUP_MENU_KIND=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "LOKI_SETUP_CONSOLE_HELPER=1", "TERM=xterm-256color")
			menuRoot := t.TempDir() + "\\untouched"
			if test.menu {
				cmd.Env = append(cmd.Env, "LOKI_SETUP_MENU_ROOT="+menuRoot, "LOKI_SETUP_MENU_KIND="+test.name)
			}
			if err := console.Start(cmd); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			ready := make(chan struct{}, 1)
			result := make(chan string, 1)
			transcript := make(chan string, 1)
			go func() {
				var screen strings.Builder
				buffer := make([]byte, 4096)
				signaled := false
				reported := false
				for {
					n, err := console.Read(buffer)
					screen.Write(buffer[:n])
					text := ansi.Strip(screen.String())
					readyText := "coordination"
					if test.name == "home" {
						readyText = "Manage tools"
					} else if test.name == "fresh" {
						readyText = "Choose execution host"
					} else if strings.HasPrefix(test.name, "working-") {
						readyText = "[RESULT] Fixture action complete"
					}
					if !signaled && strings.Contains(text, readyText) {
						signaled = true
						ready <- struct{}{}
					}
					if start := strings.Index(text, "LOKI_SETUP_RESULT:"); !reported && start >= 0 {
						line := text[start+len("LOKI_SETUP_RESULT:"):]
						if end := strings.IndexByte(line, '\n'); end >= 0 {
							result <- strings.TrimSpace(line[:end])
							transcript <- screen.String()
							reported = true
						}
					}
					if err != nil {
						return
					}
				}
			}()
			select {
			case <-ready:
			case <-time.After(15 * time.Second):
				t.Fatal("Windows checkbox list did not render")
			}
			if _, err := console.Write([]byte(test.keys)); err != nil {
				t.Fatal(err)
			}
			select {
			case raw := <-result:
				var actual setupConsoleResult
				if err := json.Unmarshal([]byte(raw), &actual); err != nil || actual.Error != "" || actual.Mask != test.mask || !actual.Restored {
					t.Fatalf("Windows selection/restoration: %s (%v)", raw, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Windows keyboard confirmation/cancellation did not finish")
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(test.name, "working-") {
				assertSetupProgressTranscript(t, <-transcript)
			}
			if test.menu {
				if _, err := os.Stat(menuRoot); !os.IsNotExist(err) {
					t.Fatal("cancelled menu changed installation", err)
				}
			}
		})
	}
}
