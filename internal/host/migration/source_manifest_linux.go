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
	if manifest.Action == "prepare" {
		defer channel.Close()
		plan, err := (secret.Controller{StateDirectory: manifest.SourceRoot}).PrepareApplicationMigration(ctx, manifest.Plan.Mappings, manifest.Plan.Resources, manifest.BaseEnvironment)
		if err != nil {
			return ErrSourceSession
		}
		metadata, err := json.Marshal(plan)
		if err != nil {
			return ErrSourceSession
		}
		defer clear(metadata)
		return privateio.WriteBytes(channel, metadata, sourceManifestLimit)
	}
	if manifest.Action == "backup" {
		return ServeSourceBackup(ctx, manifest, channel)
	}
	if manifest.Action == "commit_source" || manifest.Action == "rollback_source" {
		defer channel.Close()
		source := secret.Controller{StateDirectory: manifest.SourceRoot}
		if err := source.FinishApplicationMigration(ctx, manifest.Plan.Revision, manifest.Operation, manifest.Action == "commit_source"); err != nil {
			return &sourceFailure{"source_finish", err}
		}
		event := SourceEvent{Operation: manifest.Operation, Type: "source_finished"}
		return lifecycle.ExchangeSourceEvent(ctx, channel, manifest.Operation, 1, event)
	}
	session := SourceSession{Source: secret.Controller{StateDirectory: manifest.SourceRoot}, Target: admin.ApplicationClient{Socket: manifest.TargetSocket, ExpectedUID: manifest.TargetUID}, Operation: manifest.Operation, Policy: manifest.Policy, Plan: manifest.Plan, BaseEnvironment: manifest.BaseEnvironment, Writes: manifest.Writes}
	return ServeSourceTransfer(ctx, session, channel)
}
