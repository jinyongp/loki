//go:build !windows

package githubsetup

import "os/exec"

func configureBrowserProcess(*exec.Cmd) {}
