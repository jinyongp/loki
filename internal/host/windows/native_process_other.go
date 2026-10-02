//go:build !windows

package windows

import "os/exec"

func configureNativeProcess(command *exec.Cmd) {}
