//go:build linux

package management

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/host/lifecycle"
	managedcommand "loki/internal/platform/command"
)

// The helper holds /work/source.age until the host's publication ACK. Docker
// copies ciphertext as a bounded tar stream; the host creates its own 0600 file
// so a sudo Docker client cannot accidentally publish a root-owned operator file.
func (b *DockerFullBackend) copySecretSourceArchive(ctx context.Context, candidate DeploymentReservation, original lifecycle.OperationRecord, event lifecycle.SourceEvent) error {
	current, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	proof := event.Archive
	prepared := current.SecretMigration.Archive
	intent := current.SecretMigration.Intent
	if current.ID != original.ID || current.Phase != lifecycle.PhaseSnapshot || proof == nil || proof.Version != 1 || proof.Operation != lifecycle.MigrationOperationUUID(current.ID) || event.Operation != proof.Operation || proof.Revision != intent.Plan.Revision || proof.Recipient != intent.Recipient || prepared == nil || prepared.CipherSHA256 != proof.CipherSHA256 || prepared.Bytes != proof.Bytes {
		return fmt.Errorf("encrypted source publication differs from its original checkpoint")
	}
	parent := filepath.Dir(intent.BackupPath)
	directory, err := unix.Openat2(unix.AT_FDCWD, parent, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return fmt.Errorf("original operator backup directory is unavailable")
	}
	dir := os.NewFile(uintptr(directory), "operator-backup-directory")
	defer dir.Close()
	var owner unix.Stat_t
	if unix.Fstat(directory, &owner) != nil || owner.Uid != uint32(os.Geteuid()) || owner.Mode&07777 != 0700 {
		return fmt.Errorf("operator backup directory must be private and owned")
	}
	file, err := os.CreateTemp(parent, ".source-cipher-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	argv := b.dockerCommand("cp", b.containerName(candidate, "prepare")+":/work/source.age", "-")
	command := managedcommand.New(ctx, argv[0], argv[1:]...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "DOCKER_API_VERSION=1.47"}
	command.Stderr = &boundedDockerOutput{limit: 64 << 10}
	command.WaitDelay = 2 * time.Second
	stream, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		stream.Close()
		return err
	}
	cleanup, err := managedcommand.Own(command)
	if err != nil {
		managedcommand.Cleanup(command)
		_ = command.Wait()
		return err
	}
	defer cleanup()
	reader := tar.NewReader(stream)
	header, copyErr := reader.Next()
	if copyErr == nil && (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size != proof.Bytes || filepath.Base(header.Name) != "source.age") {
		copyErr = fmt.Errorf("invalid encrypted source copy envelope")
	}
	if copyErr == nil {
		_, copyErr = io.CopyN(file, reader, proof.Bytes)
	}
	if copyErr == nil {
		if _, err := reader.Next(); err != io.EOF {
			copyErr = fmt.Errorf("unexpected encrypted source copy entry")
		}
	}
	if copyErr != nil {
		stream.Close()
		managedcommand.Cleanup(command)
	}
	waitErr := command.Wait()
	if copyErr != nil {
		return copyErr
	}
	if waitErr != nil {
		return fmt.Errorf("encrypted source copy did not complete")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := verifyMigrationArchiveFile(name, prepared); err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, name, unix.AT_FDCWD, intent.BackupPath, unix.RENAME_NOREPLACE); err != nil {
		if err != unix.EEXIST || verifyMigrationArchiveFile(intent.BackupPath, prepared) != nil {
			return fmt.Errorf("original encrypted source archive was not published")
		}
	}
	return dir.Sync()
}
