//go:build linux

package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/host/lifecycle"
	managedcommand "loki/internal/platform/command"
)

type fullSourceChannel struct {
	input  io.ReadCloser
	output io.WriteCloser
}

func (c fullSourceChannel) Read(p []byte) (int, error)  { return c.input.Read(p) }
func (c fullSourceChannel) Write(p []byte) (int, error) { return c.output.Write(p) }
func (c fullSourceChannel) Close() error                { _ = c.input.Close(); return c.output.Close() }

func verifyPublishedMigrationArchive(record lifecycle.OperationRecord) error {
	archive := record.SecretMigration.Archive
	if archive == nil || !archive.Published {
		return fmt.Errorf("original encrypted source backup must be durably published")
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, record.SecretMigration.Intent.BackupPath, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return fmt.Errorf("original encrypted source backup is unavailable")
	}
	file := os.NewFile(uintptr(fd), "original-encrypted-source-backup")
	defer file.Close()
	var info unix.Stat_t
	if unix.Fstat(fd, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&07777 != 0600 || info.Uid != uint32(os.Geteuid()) || info.Nlink != 1 || info.Size != archive.Bytes {
		return fmt.Errorf("original encrypted source backup ownership or size changed")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(file, archive.Bytes+1)); err != nil || hex.EncodeToString(digest.Sum(nil)) != archive.CipherSHA256 {
		return fmt.Errorf("original encrypted source backup integrity changed")
	}
	return nil
}

// TransferSecretMigrationSource keeps legacy plaintext inside the private root
// helper and broker. The host acknowledges metadata only through its existing
// transaction journal. The helper receives no Docker socket, workspace, task
// root, target provider files or other generation.
func (b *DockerFullBackend) TransferSecretMigrationSource(ctx context.Context, candidate DeploymentReservation) error {
	operation, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	if operation.State != lifecycle.OperationApplying || operation.Phase != lifecycle.PhaseMigrate {
		return fmt.Errorf("source transfer requires the original migration phase")
	}
	if err := verifyPublishedMigrationArchive(operation); err != nil {
		return err
	}
	intent := operation.SecretMigration.Intent
	if intent.SourceRoot != "/var/lib/loki/runtime" {
		return fmt.Errorf("source root differs from the installed legacy runtime volume")
	}
	record, err := b.record(candidate)
	if err != nil {
		return err
	}
	if record.MigrationCandidate == nil || record.Preparing {
		return fmt.Errorf("original private candidate is unavailable")
	}
	sourceVolume := storeSourceVolume(b.Store)
	volumeMetadata, err := b.command(ctx, "verifying original source volume ownership", nil, "volume", "inspect", "--format", "{{json .}}", sourceVolume.Source)
	if err != nil {
		return err
	}
	var volume struct {
		Name    string
		Driver  string
		Labels  map[string]string
		Options map[string]string
	}
	if json.Unmarshal(volumeMetadata, &volume) != nil || volume.Name != sourceVolume.Source || volume.Driver != "local" || len(volume.Options) != 0 || volume.Labels[fullOwnerLabel] != b.Store.FullOwner() || volume.Labels[fullResourceLabel] != sourceVolume.Source {
		return fmt.Errorf("source volume is foreign or unsupported")
	}
	_, contract, err := b.Store.fullApplicationInputs()
	if err != nil {
		return err
	}
	manifest := lifecycle.SourceTransferManifest{Version: 1, Operation: lifecycle.MigrationOperationUUID(operation.ID), Policy: intent.Policy, SourceRoot: intent.SourceRoot, TargetSocket: "/run/loki/secrets/admin/control.sock", TargetUID: 10006, Plan: intent.Plan, BaseEnvironment: contract.Environment, Writes: operation.SecretMigration.Writes}
	projection := filepath.Join("/dev/shm", b.Store.FullOwner()+"-migration-"+operation.ID, "secrets")
	mounts := []FullMount{sourceVolume, {Kind: "bind", Source: filepath.Join(projection, "admin"), Target: "/run/loki/secrets/admin", ReadOnly: true}}
	workerBound := false
	for _, mount := range record.Specs[0].Mounts {
		if mount.Kind == "bind" && mount.ReadOnly && strings.HasPrefix(record.Worker, mount.Target+"/") {
			mounts = append(mounts, mount)
			workerBound = true
		}
	}
	if !workerBound {
		return fmt.Errorf("source helper lacks its immutable worker")
	}
	ids, err := b.containerIDs(ctx, candidate)
	if err != nil {
		return err
	}
	for _, id := range ids {
		container, err := b.inspectContainer(ctx, id)
		if err != nil {
			return err
		}
		if err := b.requireParent(candidate, container); err != nil {
			return err
		}
		if container.Config.Labels[fullServiceLabel] == "prepare" {
			if _, err := b.command(ctx, "removing interrupted owned source helper", nil, "container", "rm", "--force", id); err != nil {
				return err
			}
		}
	}
	args := []string{"run", "--rm", "--interactive", "--name", b.containerName(candidate, "prepare"), "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "1073741824", "--pids-limit", "64", "--log-driver", "none", "--entrypoint", record.Worker}
	for _, label := range b.labels(candidate, "prepare") {
		args = append(args, "--label", label)
	}
	for _, mount := range mounts {
		argument, err := dockerMountArgument(mount)
		if err != nil {
			return err
		}
		args = append(args, "--mount", argument)
	}
	args = append(args, record.Specs[0].Image, "host", "secret-migration-source")
	argv := b.dockerCommand(args...)
	command := managedcommand.New(ctx, argv[0], argv[1:]...)
	environment := []string{}
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if name != "DOCKER_HOST" && name != "DOCKER_CONTEXT" && name != "DOCKER_API_VERSION" {
			environment = append(environment, item)
		}
	}
	command.Env = append(environment, "DOCKER_API_VERSION=1.47")
	command.WaitDelay = 2 * time.Second
	command.Stderr = &boundedDockerOutput{limit: 64 << 10}
	input, err := command.StdinPipe()
	if err != nil {
		return err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		return err
	}
	if err := command.Start(); err != nil {
		input.Close()
		output.Close()
		return err
	}
	cleanup, err := managedcommand.Own(command)
	if err != nil {
		managedcommand.Cleanup(command)
		_ = command.Wait()
		return err
	}
	defer cleanup()
	channel := fullSourceChannel{input: output, output: input}
	defer channel.Close()
	transferErr := lifecycle.WriteSourceTransferManifest(channel, manifest)
	if transferErr == nil {
		scope := ctx.Value(fullMigrationMaintenanceKey{}).(*fullMigrationMaintenanceScope)
		transferErr = lifecycle.CheckpointSourceTransfer(ctx, channel, manifest.Operation, func(event lifecycle.SourceEvent) error {
			return scope.journal.CheckpointMigrationSourceEvent(operation.ID, event, time.Now())
		})
	}
	channel.Close()
	if transferErr != nil {
		managedcommand.Cleanup(command)
	}
	waitErr := command.Wait()
	if transferErr != nil {
		return transferErr
	}
	if waitErr != nil {
		return fmt.Errorf("source helper did not complete; resume the original host migration")
	}
	return nil
}

func storeSourceVolume(store Store) FullMount {
	return FullMount{Kind: "volume", Source: store.FullOwner() + "-data-runtime-audit", Target: "/var/lib/loki/runtime"}
}
