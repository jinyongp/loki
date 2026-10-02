//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"loki/internal/progress"
)

func TestWindowsConsoleOutputKeepsLineOrigins(t *testing.T) {
	// A separate console keeps CI's redirected handles and the invoking
	// terminal untouched. The child measures native cursor coordinates.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestWindowsConsoleOutputHelper$", "-test.v")
	command.Env = append(os.Environ(), "LOKI_TEST_CONSOLE_CHILD=1")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native console verification: %v\n%s", err, output)
	}
	t.Logf("native console verification:\n%s", output)
}

func TestWindowsConsoleOutputHelper(t *testing.T) {
	if os.Getenv("LOKI_TEST_CONSOLE_CHILD") != "1" {
		return
	}
	console, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer console.Close()
	handle := windows.Handle(console.Fd())
	var original uint32
	if err := windows.GetConsoleMode(handle, &original); err != nil {
		t.Fatal(err)
	}
	defer windows.SetConsoleMode(handle, original)

	for _, mode := range []struct {
		name string
		bits uint32
	}{
		{"processed", windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_WRAP_AT_EOL_OUTPUT},
		{"virtual-terminal", windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_WRAP_AT_EOL_OUTPUT | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING},
		{"no-auto-return", windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_WRAP_AT_EOL_OUTPUT | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.DISABLE_NEWLINE_AUTO_RETURN},
	} {
		t.Run(mode.name, func(t *testing.T) {
			if err := windows.SetConsoleMode(handle, mode.bits); err != nil {
				t.Fatal(err)
			}
			if _, err := console.WriteString("\r"); err != nil {
				t.Fatal(err)
			}
			var before windows.ConsoleScreenBufferInfo
			if err := windows.GetConsoleScreenBufferInfo(handle, &before); err != nil {
				t.Fatal(err)
			}
			stdout := consoleOutputWriter(console)
			stderr := consoleOutputWriter(console)
			if stdout == console || stderr == console {
				t.Fatal("native console was not recognized as a terminal")
			}
			reporter := progress.NewLineReporter(progress.WithVerbose(stderr))
			for index, line := range []string{"Inspecting installed release...", "Checking published release...", "Downloading verified assets..."} {
				progress.Emit(reporter, progress.Event{Message: line})
				var after windows.ConsoleScreenBufferInfo
				if err := windows.GetConsoleScreenBufferInfo(handle, &after); err != nil {
					t.Fatal(err)
				}
				if after.CursorPosition.X != 0 || after.CursorPosition.Y != before.CursorPosition.Y+int16(index+1) {
					t.Fatalf("progress line %d cursor=%+v, want column 0 and row %d", index, after.CursorPosition, before.CursorPosition.Y+int16(index+1))
				}
			}
			if _, err := fmt.Fprint(stdout, "Prepared Loki update\n  Restart required: yes\r"); err != nil {
				t.Fatal(err)
			}
			if _, err := fmt.Fprint(stdout, "\n"); err != nil {
				t.Fatal(err)
			}
			var after windows.ConsoleScreenBufferInfo
			if err := windows.GetConsoleScreenBufferInfo(handle, &after); err != nil {
				t.Fatal(err)
			}
			if after.CursorPosition.X != 0 || after.CursorPosition.Y != before.CursorPosition.Y+5 {
				t.Fatalf("summary cursor=%+v, want column 0 and row %d", after.CursorPosition, before.CursorPosition.Y+5)
			}
		})
	}
}
