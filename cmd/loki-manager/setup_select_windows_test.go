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
	options, err := parseSetup(t.Context(), nil, os.Stdin, os.Stderr)
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
	}{
		{"multiple", "\x1b[B \x1b[B\x1b[B \r", 10},
		{"select all", "\x01\r", 255},
		{"empty", "\r", 0},
		{"cancel checked choice", " \x03", 0},
		{"escape", " \x1b", 0},
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
				if !strings.HasPrefix(upper, "TERM=") && !strings.HasPrefix(upper, "LOKI_SETUP_CONSOLE_HELPER=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "LOKI_SETUP_CONSOLE_HELPER=1", "TERM=xterm-256color")
			if err := console.Start(cmd); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			ready := make(chan struct{}, 1)
			result := make(chan string, 1)
			go func() {
				var screen strings.Builder
				buffer := make([]byte, 4096)
				signaled := false
				reported := false
				for {
					n, err := console.Read(buffer)
					screen.Write(buffer[:n])
					text := ansi.Strip(screen.String())
					if !signaled && strings.Contains(text, "coordination") {
						signaled = true
						ready <- struct{}{}
					}
					if start := strings.Index(text, "LOKI_SETUP_RESULT:"); !reported && start >= 0 {
						line := text[start+len("LOKI_SETUP_RESULT:"):]
						if end := strings.IndexByte(line, '\n'); end >= 0 {
							result <- strings.TrimSpace(line[:end])
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
		})
	}
}
