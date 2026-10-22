//go:build linux

package migration

import (
	"context"
	"io"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
)

// ServeSourceTransfer belongs to the privileged decoder; its metadata codec
// and canonical journal handlers do not import this owner or its legacy vault.
func ServeSourceTransfer(ctx context.Context, session SourceSession, channel io.ReadWriteCloser) error {
	if channel == nil || !controlpolicy.ApplicationUUID.MatchString(session.Operation) {
		return ErrSourceSession
	}
	defer channel.Close()
	stop := context.AfterFunc(ctx, func() { channel.Close() })
	defer stop()
	sequence := uint64(0)
	session.Checkpoint = func(event SourceEvent) error {
		sequence++
		return lifecycle.ExchangeSourceEvent(ctx, channel, session.Operation, sequence, event)
	}
	return session.TransferAndSeal(ctx)
}

func CheckpointSourceTransfer(ctx context.Context, channel io.ReadWriteCloser, operation string, checkpoint func(SourceEvent) error) error {
	return lifecycle.CheckpointSourceTransfer(ctx, channel, operation, checkpoint)
}
