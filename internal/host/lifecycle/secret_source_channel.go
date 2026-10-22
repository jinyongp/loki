package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
)

var ErrSourceSession = errors.New("original migration source session did not complete")

// SourceEvent contains only immutable host metadata; the portable manager
// imports this codec without reaching the privileged legacy source decoder.
type SourceEvent struct {
	Operation string                                `json:"operation"`
	Type      string                                `json:"type"`
	Write     *MigrationWriteCheckpoint             `json:"write,omitempty"`
	Binding   *appvalues.MigrationComparisonBinding `json:"binding,omitempty"`
	Attempt   string                                `json:"attempt,omitempty"`
	Count     int                                   `json:"count,omitempty"`
	Archive   *appvalues.SourceBackupProof          `json:"archive,omitempty"`
}

type sourceEnvelope struct {
	Version  int         `json:"version"`
	Sequence uint64      `json:"sequence"`
	Event    SourceEvent `json:"event"`
}

type sourceAcknowledgement struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	Sequence  uint64 `json:"sequence"`
	Digest    string `json:"digest"`
	Accepted  bool   `json:"accepted"`
}

func sourceEventDigest(frame []byte) string {
	sum := sha256.Sum256(frame)
	return hex.EncodeToString(sum[:])
}

// ExchangeSourceEvent waits for the exact canonical metadata ACK before a
// helper performs its corresponding provider effect or private approval.
func ExchangeSourceEvent(ctx context.Context, channel io.ReadWriter, operation string, sequence uint64, event SourceEvent) error {
	if !controlpolicy.ApplicationUUID.MatchString(operation) || sequence == 0 || event.Operation != operation {
		return ErrSourceSession
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	frame, err := json.Marshal(sourceEnvelope{Version: 1, Sequence: sequence, Event: event})
	if err != nil {
		return ErrSourceSession
	}
	defer clear(frame)
	if privateio.WriteBytes(channel, frame, privateio.MaxControlFrame) != nil {
		return ErrSourceSession
	}
	var ack sourceAcknowledgement
	if privateio.ReadControl(channel, &ack) != nil || ack.Version != 1 || ack.Operation != operation || ack.Sequence != sequence || ack.Digest != sourceEventDigest(frame) || !ack.Accepted {
		return ErrSourceSession
	}
	return nil
}

// CheckpointSourceTransfer runs under the existing host owner lock. It cannot
// read source values or approve a provider write until the exact event is durable.
// The metadata digest binds its ACK to the original frame, not a plaintext hash.
func CheckpointSourceTransfer(ctx context.Context, channel io.ReadWriteCloser, operation string, checkpoint func(SourceEvent) error) error {
	if channel == nil || checkpoint == nil || !controlpolicy.ApplicationUUID.MatchString(operation) {
		return ErrSourceSession
	}
	defer channel.Close()
	stop := context.AfterFunc(ctx, func() { channel.Close() })
	defer stop()
	const maxEvents = 4*128*512 + 1024
	for sequence := uint64(1); sequence <= maxEvents; sequence++ {
		frame, err := privateio.ReadBytes(channel, privateio.MaxControlFrame)
		if err != nil {
			return ErrSourceSession
		}
		var envelope sourceEnvelope
		err = privateio.Decode(frame, &envelope)
		digest := sourceEventDigest(frame)
		clear(frame)
		if err != nil || envelope.Version != 1 || envelope.Sequence != sequence || envelope.Event.Operation != operation {
			return ErrSourceSession
		}
		checkpointErr := checkpoint(envelope.Event)
		ack := sourceAcknowledgement{Version: 1, Operation: operation, Sequence: sequence, Digest: digest, Accepted: checkpointErr == nil}
		ackErr := privateio.WriteControl(channel, ack)
		if checkpointErr != nil {
			return checkpointErr
		}
		if ackErr != nil {
			return ErrSourceSession
		}
		if envelope.Event.Type == "target_sealed" || envelope.Event.Type == "source_backup_complete" {
			return nil
		}
	}
	return ErrSourceSession
}
