//go:build linux || darwin

package command

import (
	"context"
	"os/exec"
	"sync"
	"syscall"
)

func New(ctx context.Context, binary string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, binary, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	return c
}

func Cleanup(c *exec.Cmd) {
	if c.Process != nil {
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}

func Own(c *exec.Cmd) (func(), error) {
	var once sync.Once
	return func() { once.Do(func() { Cleanup(c) }) }, nil
}
