//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func keepalive(distribution string) int {
	root := os.Getenv("SystemRoot")
	if !filepath.IsAbs(root) {
		return 1
	}
	command := exec.Command(filepath.Join(root, "System32", "wsl.exe"),
		"-d", distribution, "--exec", "/usr/bin/sleep", "infinity")
	// The companion is built with the GUI subsystem, and its child receives no
	// console. Neither process belongs to the terminal that requested startup.
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if err := command.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}
