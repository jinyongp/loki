//go:build windows

package githubsetup

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureBrowserProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
