//go:build linux

package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loki/internal/control/appvalues"
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
	return verifyMigrationArchiveFile(record.SecretMigration.Intent.BackupPath, archive)
}

func verifyMigrationArchiveFile(path string, archive *lifecycle.MigrationArchiveCheckpoint) error {
	return lifecycle.VerifyMigrationSourceArchive(path, archive)
}

// TransferSecretMigrationSource keeps legacy plaintext inside the private root
// helper and broker. The host acknowledges metadata only through its existing
// transaction journal. The helper receives no Docker socket, workspace, task
// root, target provider files or other generation.
func (b *DockerFullBackend) TransferSecretMigrationSource(ctx context.Context, candidate DeploymentReservation) error {
	return b.runSecretMigrationSource(ctx, candidate, "transfer")
}

func (b *DockerFullBackend) BackupSecretMigrationSource(ctx context.Context, candidate DeploymentReservation) error {
	return b.runSecretMigrationSource(ctx, candidate, "backup")
}

// FinishSecretMigrationSource acknowledges the actual source gate only after
// the original host owner has a durable successful/rolled-back result. Job and
// app admission still require their own ACK before the host release checkpoint.
func (b *DockerFullBackend) FinishSecretMigrationSource(ctx context.Context, candidate DeploymentReservation) error {
	r, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	if r.State == lifecycle.OperationSucceeded {
		return b.runSecretMigrationSource(ctx, candidate, "commit_source")
	}
	if r.State == lifecycle.OperationRolledBack {
		return b.runSecretMigrationSource(ctx, candidate, "rollback_source")
	}
	return fmt.Errorf("source release requires the original terminal host checkpoint")
}

func (b *DockerFullBackend) runSecretMigrationSource(ctx context.Context, candidate DeploymentReservation, action string) error {
	backup := action == "backup"
	finish := action == "commit_source" || action == "rollback_source"
	if action != "transfer" && !backup && !finish {
		return fmt.Errorf("unsupported source operation")
	}
	operation, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	if finish {
		if action == "commit_source" && operation.State != lifecycle.OperationSucceeded || action == "rollback_source" && operation.State != lifecycle.OperationRolledBack {
			return fmt.Errorf("source release differs from the original terminal result")
		}
	} else {
		if operation.SecretMigration.RollbackRequested {
			return fmt.Errorf("original migration is committed to rollback recovery")
		}
		if operation.State != lifecycle.OperationApplying || !backup && operation.Phase != lifecycle.PhaseMigrate {
			return fmt.Errorf("source transfer requires the original migration phase")
		}
		if backup && operation.Phase != lifecycle.PhaseAdmitted && operation.Phase != lifecycle.PhaseSnapshot && operation.Phase != lifecycle.PhaseMigrate {
			return fmt.Errorf("source backup cannot replace an archive after cutover")
		}
	}
	if finish {
		// Terminal disposition is verified by the source's original UUID and
		// fence. No ciphertext regeneration or target access occurs here.
	} else if !backup {
		if err := verifyPublishedMigrationArchive(operation); err != nil {
			return err
		}
	} else if archive := operation.SecretMigration.Archive; archive != nil && verifyMigrationArchiveFile(operation.SecretMigration.Intent.BackupPath, archive) == nil {
		directory, err := os.Open(filepath.Dir(operation.SecretMigration.Intent.BackupPath))
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if operation.Phase == lifecycle.PhaseMigrate {
			return nil
		}
		scope := ctx.Value(fullMigrationMaintenanceKey{}).(*fullMigrationMaintenanceScope)
		if !archive.Published {
			if _, err := scope.journal.CheckpointMigrationArchive(operation.ID, archive.CipherSHA256, archive.Bytes, true, time.Now()); err != nil {
				return err
			}
		}
		_, err = scope.journal.AdvanceSecretMigration(operation.ID, lifecycle.PhaseMigrate, time.Now())
		return err
	} else if archive != nil && archive.Published {
		return fmt.Errorf("published original source archive is unavailable; do not regenerate it")
	}
	intent := operation.SecretMigration.Intent
	if intent.SourceRoot != "/var/lib/loki/runtime" {
		return fmt.Errorf("source root differs from the installed legacy runtime volume")
	}
	var record fullDockerRecord
	if finish {
		// A failed preparation may leave no candidate service record. Terminal
		// source disposition needs only the original immutable core payload,
		// never the candidate's projection or target generation.
		resources, err := b.Store.FullResources()
		if err != nil {
			return err
		}
		record.Worker, err = resources.ServiceProgram("runtime-core", "loki")
		if err != nil {
			return err
		}
		image, err := resources.Image("runtime-core", "service")
		if err != nil {
			return err
		}
		spec := fullContainerSpec{Image: image}
		for _, program := range resources.Plan.Programs {
			if program.Module == "runtime-core" {
				spec.Mounts = append(spec.Mounts, fullModuleMount(program))
			}
		}
		record.Specs = []fullContainerSpec{spec}
	} else {
		record, err = b.record(candidate)
		if err != nil {
			return err
		}
		if record.MigrationCandidate == nil || record.Preparing {
			return fmt.Errorf("original private candidate is unavailable")
		}
	}
	sourceVolume, err := b.verifiedSecretSourceVolume(ctx)
	if err != nil {
		return err
	}
	if !finish {
		if err := b.requireOfflineSecretSource(ctx, sourceVolume, candidate); err != nil {
			return err
		}
	}
	_, contract, err := b.Store.fullApplicationInputs()
	if err != nil {
		return err
	}
	manifest := lifecycle.SourceTransferManifest{Version: 1, Operation: lifecycle.MigrationOperationUUID(operation.ID), Policy: intent.Policy, SourceRoot: intent.SourceRoot, TargetSocket: "/run/loki/secrets/admin/control.sock", TargetUID: 10006, Plan: intent.Plan, BaseEnvironment: contract.Environment, Writes: operation.SecretMigration.Writes}
	if finish {
		manifest.Action, manifest.TargetSocket, manifest.TargetUID = action, "", 0
		manifest.Writes = []lifecycle.MigrationWriteCheckpoint{}
	}
	if backup {
		manifest.Action, manifest.Recipient, manifest.TargetSocket, manifest.TargetUID = "backup", intent.Recipient, "", 0
		manifest.ArchivePath = "/work/source.age"
		if archive := operation.SecretMigration.Archive; archive != nil {
			manifest.PreviousBackup = &appvalues.SourceBackupProof{Version: 1, Operation: manifest.Operation, Revision: intent.Plan.Revision, Recipient: intent.Recipient, CipherSHA256: archive.CipherSHA256, Bytes: archive.Bytes}
		}
	}
	projection := filepath.Join("/dev/shm", b.Store.FullOwner()+"-migration-"+operation.ID, "secrets")
	mounts := []FullMount{sourceVolume}
	if backup {
		cipher := storeSourceCipherVolume(b.Store, operation.ID)
		if err := b.ownedVolume(ctx, cipher); err != nil {
			return err
		}
		mounts = append(mounts, cipher)
	} else if !finish {
		mounts = append(mounts, FullMount{Kind: "bind", Source: filepath.Join(projection, "admin"), Target: "/run/loki/secrets/admin", ReadOnly: true})
	}
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
	memory := "1073741824"
	if backup || finish {
		memory = "2147483648"
	}
	args := []string{"run", "--rm", "--interactive", "--name", b.containerName(candidate, "prepare"), "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", memory, "--pids-limit", "64", "--log-driver", "none", "--entrypoint", record.Worker}
	if backup || finish {
		args = append(args, "--cap-add", "CHOWN", "--cap-add", "FOWNER")
	}
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
	return b.exchangeSecretSourceHelper(ctx, args, func(channel fullSourceChannel) error {
		if err := lifecycle.WriteSourceTransferManifest(channel, manifest); err != nil {
			return err
		}
		scope := ctx.Value(fullMigrationMaintenanceKey{}).(*fullMigrationMaintenanceScope)
		return lifecycle.CheckpointSourceTransfer(ctx, channel, manifest.Operation, func(event lifecycle.SourceEvent) error {
			if finish {
				if event != (lifecycle.SourceEvent{Operation: manifest.Operation, Type: "source_finished"}) {
					return lifecycle.ErrSourceSession
				}
				return nil
			}
			if event.Type == "archive_published" {
				if err := b.copySecretSourceArchive(ctx, candidate, operation, event); err != nil {
					return err
				}
			}
			return scope.journal.CheckpointMigrationSourceEvent(operation.ID, event, time.Now())
		})
	})
}

func (b *DockerFullBackend) exchangeSecretSourceHelper(ctx context.Context, args []string, exchange func(fullSourceChannel) error) error {
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
	diagnostic := &boundedDockerOutput{limit: 64 << 10}
	command.Stderr = diagnostic
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
	transferErr := exchange(channel)
	channel.Close()
	if transferErr != nil {
		managedcommand.Cleanup(command)
	}
	waitErr := command.Wait()
	if transferErr != nil {
		for _, stage := range []string{"freeze", "freeze_ack", "archive", "archive_ack", "complete_ack", "transfer", "source_finish"} {
			if strings.Contains(diagnostic.String(), "source transfer did not complete at "+stage+";") {
				return fmt.Errorf("owned source helper stopped at %s: %w", stage, transferErr)
			}
		}
		return transferErr
	}
	if waitErr != nil {
		return fmt.Errorf("source helper did not complete; resume the original host migration")
	}
	return nil
}

func (b *DockerFullBackend) verifiedSecretSourceVolume(ctx context.Context) (FullMount, error) {
	return b.verifiedMigrationVolume(ctx, storeSourceVolume(b.Store))
}

func (b *DockerFullBackend) verifiedMigrationVolume(ctx context.Context, source FullMount) (FullMount, error) {
	metadata, err := b.command(ctx, "verifying original source volume ownership", nil, "volume", "inspect", "--format", "{{json .}}", source.Source)
	if err != nil {
		return FullMount{}, err
	}
	var volume struct {
		Name, Driver string
		Labels       map[string]string
		Options      map[string]string
	}
	if json.Unmarshal(metadata, &volume) != nil || volume.Name != source.Source || volume.Driver != "local" || len(volume.Options) != 0 || volume.Labels[fullOwnerLabel] != b.Store.FullOwner() || volume.Labels[fullResourceLabel] != source.Source {
		return FullMount{}, fmt.Errorf("source volume is foreign or unsupported")
	}
	return source, nil
}

func (b *DockerFullBackend) requireOfflineSecretSource(ctx context.Context, source FullMount, candidate DeploymentReservation) error {
	data, err := b.command(ctx, "verifying source writers are stopped", nil, "container", "ls", "--filter", "volume="+source.Source, "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	ids, err := dockerLines(data)
	if err != nil {
		return err
	}
	for _, id := range ids {
		container, err := b.inspectContainer(ctx, id)
		if err != nil {
			return err
		}
		// Only the original interrupted helper may still hold this mount.
		// The caller removes that exact owned helper before starting another.
		if b.requireParent(candidate, container) != nil || container.Config.Labels[fullServiceLabel] != "prepare" {
			return fmt.Errorf("stop original source writers under the host maintenance fence before migration")
		}
	}
	return nil
}

func storeSourceVolume(store Store) FullMount {
	return FullMount{Kind: "volume", Source: store.FullOwner() + "-data-runtime-audit", Target: "/var/lib/loki/runtime"}
}

func storeSourceCipherVolume(store Store, operation string) FullMount {
	return FullMount{Kind: "volume", Source: store.FullOwner() + "-migration-" + operation + "-source-cipher", Target: "/work"}
}
