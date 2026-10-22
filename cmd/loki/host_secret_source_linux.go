//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	hostmigration "loki/internal/host/migration"
)

type sourceHelperStdio struct {
	io.Reader
	io.Writer
}

func (s sourceHelperStdio) Close() error {
	var failures []error
	if input, ok := s.Reader.(io.Closer); ok {
		failures = append(failures, input.Close())
	}
	if output, ok := s.Writer.(io.Closer); ok {
		failures = append(failures, output.Close())
	}
	return errors.Join(failures...)
}

// The owned root helper reads only its verified source mount and uses the
// broker's finite private administrator socket. stdout is exclusively framed
// host metadata; original values never pass through the host process or argv.
func runHostSecretSource(args []string, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 || len(args) != 0 {
		fmt.Fprintln(stderr, "source transfer requires its owned root administrator helper")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if hostmigration.ServeSourceTransferManifest(ctx, sourceHelperStdio{Reader: os.Stdin, Writer: stdout}) != nil {
		fmt.Fprintln(stderr, "source transfer did not complete; resume the original host operation")
		return 1
	}
	return 0
}
