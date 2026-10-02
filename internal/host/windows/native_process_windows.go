//go:build windows

package windows

import (
	"os/exec"
	"syscall"

	winapi "golang.org/x/sys/windows"
)

func configureNativeProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: winapi.CREATE_NO_WINDOW}
}
