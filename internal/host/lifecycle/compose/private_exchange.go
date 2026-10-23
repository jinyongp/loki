package compose

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	managedcommand "loki/internal/platform/command"
)

// PrivateExchangeRunner is for the finite source-helper framing protocol.
// stdout carries metadata/ACK frames only; stderr never joins that channel or
// enters host diagnostics. There is no generic public command forwarding API.
type PrivateExchangeRunner interface {
	ExchangePrivate(context.Context, []string, []string, func(io.ReadWriteCloser) error) error
}

type privateExchangeChannel struct {
	reader io.ReadCloser
	writer io.WriteCloser
}

func (c privateExchangeChannel) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c privateExchangeChannel) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c privateExchangeChannel) Close() error {
	return errors.Join(c.reader.Close(), c.writer.Close())
}

func (r ExecRunner) ExchangePrivate(ctx context.Context, env, args []string, exchange func(io.ReadWriteCloser) error) error {
	if exchange == nil {
		return errors.New("private source exchange is unavailable")
	}
	executable := strings.TrimSpace(r.Executable)
	if executable == "" {
		executable = "docker"
	}
	command := managedcommand.New(ctx, executable, append(append([]string(nil), r.Prefix...), args...)...)
	command.Env = append(os.Environ(), env...)
	command.Stderr = io.Discard
	command.WaitDelay = 2 * time.Second
	input, err := command.StdinPipe()
	if err != nil {
		return err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		return err
	}
	channel := privateExchangeChannel{reader: output, writer: input}
	defer channel.Close()
	if err := command.Start(); err != nil {
		return errors.New("private source helper could not start")
	}
	cleanup, err := managedcommand.Own(command)
	if err != nil {
		managedcommand.Cleanup(command)
		_ = command.Wait()
		return errors.New("private source helper ownership was not established")
	}
	defer cleanup()
	stop := context.AfterFunc(ctx, func() { channel.Close() })
	defer stop()
	exchangeErr := exchange(channel)
	channel.Close()
	if exchangeErr != nil {
		managedcommand.Cleanup(command)
	}
	waitErr := command.Wait()
	if exchangeErr != nil {
		return exchangeErr
	}
	if waitErr != nil {
		return errors.New("private source helper did not complete")
	}
	return nil
}
