//go:build linux

package migration

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"loki/internal/control/appvalues"
	"loki/internal/host/lifecycle"
	secret "loki/modules/secrets"
)

type sourceFailure struct {
	stage string
	err   error
}

func (e *sourceFailure) Error() string { return "source helper did not complete at " + e.stage }
func (e *sourceFailure) Unwrap() error { return e.err }

// SourceFailureStage returns a finite public stage, never an underlying source
// or provider error that might contain private bytes.
func SourceFailureStage(err error) string {
	var failure *sourceFailure
	if errors.As(err, &failure) {
		return failure.stage
	}
	return "transfer"
}

// ServeSourceBackup writes only an operator-encrypted recovery artifact into
// the helper's private backup mount. The host copies that finite file before it
// acknowledges publication. Plaintext source bytes never use stdout/stdin.
func ServeSourceBackup(ctx context.Context, manifest SourceTransferManifest, channel io.ReadWriteCloser) error {
	defer channel.Close()
	if manifest.ArchivePath == "/work/source.age" {
		if err := prepareSourceCipherVolume(); err != nil {
			return &sourceFailure{"archive", err}
		}
	}
	sequence := uint64(0)
	emit := func(event SourceEvent) error {
		sequence++
		event.Operation = manifest.Operation
		return lifecycle.ExchangeSourceEvent(ctx, channel, manifest.Operation, sequence, event)
	}
	source := secret.Controller{StateDirectory: manifest.SourceRoot}
	if err := source.BeginApplicationMigration(ctx, manifest.Plan.Revision, manifest.Operation); err != nil {
		return &sourceFailure{"freeze", err}
	}
	if err := emit(SourceEvent{Type: "source_frozen"}); err != nil {
		return &sourceFailure{"freeze_ack", err}
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
		return &sourceFailure{"archive", err}
	}
	metadata := appvalues.SourceBackupProof(proof)
	if err := emit(SourceEvent{Type: "archive_published", Archive: &metadata}); err != nil {
		return &sourceFailure{"archive_ack", err}
	}
	if err := emit(SourceEvent{Type: "source_backup_complete"}); err != nil {
		return &sourceFailure{"complete_ack", err}
	}
	return nil
}

// Docker cannot copy a running container's tmpfs. The original operation's
// owned volume stores ciphertext only, surviving a process/host interruption.
// Adopt only its empty root-owned default directory, never foreign contents.
func prepareSourceCipherVolume() error {
	fd, err := unix.Openat2(unix.AT_FDCWD, "/work", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return ErrSourceSession
	}
	directory := os.NewFile(uintptr(fd), "original-source-cipher-directory")
	defer directory.Close()
	var info unix.Stat_t
	if unix.Fstat(fd, &info) != nil || info.Uid != 0 || info.Gid != 0 {
		return ErrSourceSession
	}
	if info.Mode&07777 == 0700 {
		return nil
	}
	if info.Mode&07777 != 0755 {
		return ErrSourceSession
	}
	entries, err := directory.ReadDir(1)
	if len(entries) != 0 || err != io.EOF || unix.Fchmod(fd, 0700) != nil {
		return ErrSourceSession
	}
	return directory.Sync()
}
