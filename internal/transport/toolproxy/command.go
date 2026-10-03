package toolproxy

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/platform/command"
	"os/exec"
	"sync"
	"time"
)

type ownedTransport struct{ command *exec.Cmd }
type ownedConnection struct {
	mcp.Connection
	command *exec.Cmd
	cleanup func()
	once    sync.Once
	err     error
}

func (t ownedTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	stdout, err := t.command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := t.command.StdinPipe()
	if err != nil {
		stdout.Close()
		return nil, err
	}
	if err := t.command.Start(); err != nil {
		stdout.Close()
		stdin.Close()
		return nil, err
	}
	cleanup, err := command.Own(t.command)
	if err != nil {
		command.Cleanup(t.command)
		t.command.Wait()
		stdout.Close()
		stdin.Close()
		return nil, err
	}
	connection, err := (&mcp.IOTransport{Reader: stdout, Writer: stdin}).Connect(ctx)
	if err != nil {
		cleanup()
		t.command.Wait()
		return nil, err
	}
	return &ownedConnection{Connection: connection, command: t.command, cleanup: cleanup}, nil
}

func (c *ownedConnection) Close() error {
	c.once.Do(func() {
		c.err = c.Connection.Close()
		done := make(chan struct{})
		go func() { _ = c.command.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			c.cleanup()
			<-done
		}
		c.cleanup()
	})
	return c.err
}
