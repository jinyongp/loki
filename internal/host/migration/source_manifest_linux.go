//go:build linux

package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"loki/internal/admin"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/privateio"
	secret "loki/modules/secrets"
)

const sourceManifestLimit = lifecycle.MaxSourceManifestBytes

// SourceTransferManifest is public metadata sent by the existing host owner to
// its private helper. Fixed source/socket paths refer to that helper's verified
// mounts. Source keys, values, passphrases and task contexts have no fields here.
type SourceTransferManifest = lifecycle.SourceTransferManifest

func WriteSourceTransferManifest(channel io.Writer, m SourceTransferManifest) error {
	return lifecycle.WriteSourceTransferManifest(channel, m)
}

// ServeSourceTransferManifest accepts only the host's canonical typed metadata
// encoding before any source access. It uses a separate larger metadata frame
// because complete legacy mappings may exceed the ordinary 128KiB controls.
// Subsequent checkpoint events and ACKs retain their normal control bound.
func ServeSourceTransferManifest(ctx context.Context, channel io.ReadWriteCloser) error {
	if channel == nil {
		return ErrSourceSession
	}
	stop := context.AfterFunc(ctx, func() { channel.Close() })
	defer stop()
	encoded, err := privateio.ReadBytes(channel, sourceManifestLimit)
	if err != nil {
		channel.Close()
		return ErrSourceSession
	}
	defer clear(encoded)
	var manifest SourceTransferManifest
	if json.Unmarshal(encoded, &manifest) != nil || !manifest.Valid() {
		channel.Close()
		return ErrSourceSession
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		channel.Close()
		return ErrSourceSession
	}
	clear(canonical)
	session := SourceSession{Source: secret.Controller{StateDirectory: manifest.SourceRoot}, Target: admin.ApplicationClient{Socket: manifest.TargetSocket, ExpectedUID: manifest.TargetUID}, Operation: manifest.Operation, Policy: manifest.Policy, Plan: manifest.Plan, BaseEnvironment: manifest.BaseEnvironment, Writes: manifest.Writes}
	return ServeSourceTransfer(ctx, session, channel)
}
