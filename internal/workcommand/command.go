// Package workcommand defines bounded command execution for scoped consumers.
// The composition root supplies a backend; this contract initializes none.
package workcommand

import (
	"context"
	"time"
)

type Request struct {
	CWD       string
	Argv      []string
	Input     []byte
	Timeout   time.Duration
	MaxOutput int
}

type Result struct {
	ExitCode  int
	Output    string
	Raw       []byte
	Truncated bool
	TimedOut  bool
	Canceled  bool
}

type Runner interface {
	Run(context.Context, Request) (Result, error)
}
