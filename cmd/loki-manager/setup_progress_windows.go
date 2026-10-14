package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func setupProgressOutput(terminal *os.File) (func(), error) {
	handle := windows.Handle(terminal.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return nil, err
	}
	// Forms restore the console mode on exit, so progress must enable VT again.
	if err := windows.SetConsoleMode(handle, (mode|windows.ENABLE_PROCESSED_OUTPUT|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)&^windows.DISABLE_NEWLINE_AUTO_RETURN); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(handle, mode) }, nil
}
