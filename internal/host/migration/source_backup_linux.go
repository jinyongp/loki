//go:build linux

package migration

import (
	"context"
	"io"

	"loki/internal/control/appvalues"
	"loki/internal/host/lifecycle"
	secret "loki/modules/secrets"
)

// ServeSourceBackup writes only an operator-encrypted recovery artifact into
// the helper's private backup mount. The host copies that finite file before it
// acknowledges publication. Plaintext source bytes never use stdout/stdin.
func ServeSourceBackup(ctx context.Context, manifest SourceTransferManifest, channel io.ReadWriteCloser) error {
	defer channel.Close()
	sequence := uint64(0)
	emit := func(event SourceEvent) error {
		sequence++
		event.Operation = manifest.Operation
		return lifecycle.ExchangeSourceEvent(ctx, channel, manifest.Operation, sequence, event)
	}
	source := secret.Controller{StateDirectory: manifest.SourceRoot}
	if err := source.BeginApplicationMigration(ctx, manifest.Plan.Revision, manifest.Operation); err != nil {
		return err
	}
	if err := emit(SourceEvent{Type: "source_frozen"}); err != nil {
		return err
	}
	var previous *secret.MigrationBackupProof
	if manifest.PreviousBackup != nil {
		proof := secret.MigrationBackupProof(*manifest.PreviousBackup)
		previous = &proof
	}
	proof, err := source.BackupApplicationMigration(ctx, manifest.Plan.Revision, manifest.Operation, manifest.Recipient, manifest.ArchivePath, previous, func(proof secret.MigrationBackupProof) error {
		metadata := appvalues.SourceBackupProof(proof)
		return emit(SourceEvent{Type: "archive_prepared", Archive: &metadata})
	})
	if err != nil {
		return err
	}
	metadata := appvalues.SourceBackupProof(proof)
	if err := emit(SourceEvent{Type: "archive_published", Archive: &metadata}); err != nil {
		return err
	}
	return emit(SourceEvent{Type: "source_backup_complete"})
}
