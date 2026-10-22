//go:build linux

// Package jobbootstrap consumes one private environment frame, replaces stdin
// with /dev/null and execs the already-selected command. It has no vault access.
package jobbootstrap

import (
	"errors"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/platform/privateio"
)

var invalid = errors.New("private bootstrap rejected input")

// Environment checks Linux's per-string and aggregate exec limits before any
// project instruction runs. The frame is the complete environment; no parent
// environment, shell or command lookup is involved.
func Environment(reader io.Reader, argv []string, stackLimit uint64) ([]string, error) {
	body, err := privateio.ReadBytes(reader, privateio.MaxPayloadFrame)
	if err != nil {
		return nil, invalid
	}
	defer clear(body)
	var input privateio.Bootstrap
	if privateio.Decode(body, &input) != nil {
		return nil, invalid
	}
	defer clear(input.Environment)
	return privateio.ValidateBootstrap(input, argv, stackLimit, os.Getpagesize())
}

func Run(argv []string) error {
	var stack unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_STACK, &stack) != nil {
		return invalid
	}
	env, err := Environment(os.Stdin, argv, stack.Cur)
	if err != nil {
		return invalid
	}
	defer clear(env)
	fd, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return invalid
	}
	if err = unix.Dup3(fd, 0, 0); err != nil {
		unix.Close(fd)
		return invalid
	}
	unix.Close(fd)
	// Do not let inherited attach/control descriptors reach project code.
	if unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC) != nil {
		return invalid
	}
	if syscall.Exec(argv[0], argv, env) != nil {
		return invalid
	}
	return nil
}
