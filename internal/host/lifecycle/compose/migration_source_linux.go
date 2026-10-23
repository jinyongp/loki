//go:build linux

package compose

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/control/appvalues"
	"loki/internal/host/lifecycle"
)

func (b *Backend) migrationCipherVolume(ctx context.Context, r lifecycle.OperationRecord) (string, error) {
	name := b.project + "-migration-" + r.ID + "-cipher"
	args := []string{"volume", "create", "--label", "io.loki.migration.owner=" + applicationOwnerID(b.root), "--label", "io.loki.migration.operation=" + r.ID, name}
	if _, err := b.runner.Run(ctx, nil, args...); err != nil {
		return "", err
	}
	body, err := b.runner.Run(ctx, nil, "volume", "inspect", name)
	var volumes []struct {
		Name, Driver string
		Labels       map[string]string
		Options      map[string]string
	}
	if err != nil || json.Unmarshal(body, &volumes) != nil || len(volumes) != 1 || volumes[0].Name != name || volumes[0].Driver != "local" || len(volumes[0].Options) != 0 || volumes[0].Labels["io.loki.migration.owner"] != applicationOwnerID(b.root) || volumes[0].Labels["io.loki.migration.operation"] != r.ID {
		return "", errComposeMigration
	}
	return name, nil
}

func (b *Backend) copyMigrationArchive(ctx context.Context, r lifecycle.OperationRecord, event lifecycle.SourceEvent) error {
	current, _, err := b.migrationRecord(ctx)
	if err != nil || current.ID != r.ID || current.Phase != lifecycle.PhaseSnapshot || event.Archive == nil || current.SecretMigration.Archive == nil {
		return errComposeMigration
	}
	proof, saved, intent := event.Archive, current.SecretMigration.Archive, current.SecretMigration.Intent
	if proof.Operation != lifecycle.MigrationOperationUUID(r.ID) || event.Operation != proof.Operation || proof.Version != 1 || proof.Revision != intent.Plan.Revision || proof.Recipient != intent.Recipient || proof.CipherSHA256 != saved.CipherSHA256 || proof.Bytes != saved.Bytes {
		return errComposeMigration
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(intent.BackupPath), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), "original-operator-backup")
	defer dir.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700 {
		return errComposeMigration
	}
	file, err := os.CreateTemp(filepath.Dir(intent.BackupPath), ".migration-cipher-")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	exchange, ok := b.runner.(PrivateExchangeRunner)
	if !ok {
		return errComposeMigration
	}
	err = exchange.ExchangePrivate(ctx, nil, []string{"cp", b.migrationContainerName(r, "source") + ":/work/source.age", "-"}, func(channel io.ReadWriteCloser) error {
		reader := tar.NewReader(channel)
		header, err := reader.Next()
		if err != nil || header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size != proof.Bytes || filepath.Base(header.Name) != "source.age" {
			return errComposeMigration
		}
		if _, err := io.CopyN(file, reader, proof.Bytes); err != nil {
			return err
		}
		if _, err := reader.Next(); err != io.EOF {
			return errComposeMigration
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := lifecycle.VerifyMigrationSourceArchive(file.Name(), saved); err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, file.Name(), unix.AT_FDCWD, intent.BackupPath, unix.RENAME_NOREPLACE); err != nil {
		if err != unix.EEXIST || lifecycle.VerifyMigrationSourceArchive(intent.BackupPath, saved) != nil {
			return errComposeMigration
		}
	}
	return dir.Sync()
}

func (b *Backend) runMigrationSource(ctx context.Context, action string) error {
	r, j, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	backup := action == "backup"
	finish := action == "commit_source" || action == "rollback_source"
	if action != "transfer" && !backup && !finish || !finish && r.SecretMigration.RollbackRequested || finish && action == "commit_source" && r.State != lifecycle.OperationSucceeded || finish && action == "rollback_source" && r.State != lifecycle.OperationRolledBack || !finish && r.State != lifecycle.OperationApplying || !finish && !backup && r.Phase != lifecycle.PhaseMigrate || backup && r.Phase != lifecycle.PhaseAdmitted && r.Phase != lifecycle.PhaseSnapshot && r.Phase != lifecycle.PhaseMigrate {
		return errComposeMigration
	}
	i := r.SecretMigration.Intent
	if !finish && !backup && (r.SecretMigration.Archive == nil || !r.SecretMigration.Archive.Published || lifecycle.VerifyMigrationSourceArchive(i.BackupPath, r.SecretMigration.Archive) != nil) {
		return errComposeMigration
	}
	if backup && r.SecretMigration.Archive != nil {
		if err := lifecycle.VerifyMigrationSourceArchive(i.BackupPath, r.SecretMigration.Archive); err == nil {
			dir, err := os.Open(filepath.Dir(i.BackupPath))
			if err != nil {
				return err
			}
			err = dir.Sync()
			dir.Close()
			if err != nil {
				return err
			}
			if r.Phase == lifecycle.PhaseMigrate {
				return nil
			}
			if !r.SecretMigration.Archive.Published {
				if _, err := j.CheckpointMigrationArchive(r.ID, r.SecretMigration.Archive.CipherSHA256, r.SecretMigration.Archive.Bytes, true, time.Now()); err != nil {
					return err
				}
			}
			_, err = j.AdvanceSecretMigration(r.ID, lifecycle.PhaseMigrate, time.Now())
			return err
		} else if r.SecretMigration.Archive.Published {
			return errComposeMigration
		}
	}
	source, err := b.migrationVolume(ctx, "runtime-state")
	if err != nil {
		return err
	}
	if err := b.cleanupMigrationSource(ctx, r, source); err != nil {
		return err
	}
	// No ordinary source writer may be running while this helper freezes or
	// transfers. Only a previous source helper from this operation is removable.
	active, err := b.runner.Run(ctx, nil, "container", "ls", "--filter", "volume="+source, "--format", "{{.ID}}")
	if err != nil || !finish && strings.TrimSpace(string(active)) != "" {
		return errComposeMigration
	}
	state, _, err := b.loadRuntime()
	if err != nil {
		return err
	}
	metadata, err := b.migrationMetadata(ctx, state, i.Plan.Resources, false)
	if err != nil {
		return err
	}
	manifest := lifecycle.SourceTransferManifest{Action: action, Version: 1, Operation: lifecycle.MigrationOperationUUID(r.ID), Policy: i.Policy, SourceRoot: i.SourceRoot, Plan: i.Plan, BaseEnvironment: metadata.BaseEnvironment, Writes: r.SecretMigration.Writes}
	args := []string{"run", "--rm", "--interactive", "--name", b.migrationContainerName(r, "source"), "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "1073741824", "--pids-limit", "64", "--log-driver", "none", "--entrypoint", "/opt/loki/bin/loki", "--mount", "type=volume,source=" + source + ",target=/var/lib/loki/runtime,volume-nocopy"}
	args = append(args, b.migrationLabels(r, "source")...)
	if backup || finish {
		args = append(args, "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--memory", "2147483648")
	}
	if backup {
		cipher, err := b.migrationCipherVolume(ctx, r)
		if err != nil {
			return err
		}
		args = append(args, "--mount", "type=volume,source="+cipher+",target=/work,volume-nocopy")
		manifest.Recipient, manifest.ArchivePath = i.Recipient, "/work/source.age"
		if a := r.SecretMigration.Archive; a != nil {
			manifest.PreviousBackup = &appvalues.SourceBackupProof{Version: 1, Operation: manifest.Operation, Revision: i.Plan.Revision, Recipient: i.Recipient, CipherSHA256: a.CipherSHA256, Bytes: a.Bytes}
		}
	} else if !finish {
		projection := filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-migration-"+r.ID, "secrets")
		args = append(args, "--mount", applicationBind(filepath.Join(projection, "admin"), "/run/loki/secrets/admin", true))
		manifest.TargetSocket, manifest.TargetUID = "/run/loki/secrets/admin/control.sock", i.Before.UID
	} else {
		manifest.Writes = []lifecycle.MigrationWriteCheckpoint{}
	}
	args = append(args, i.ContainerImage, "host", "secret-migration-source")
	exchange, ok := b.runner.(PrivateExchangeRunner)
	if !ok {
		return errComposeMigration
	}
	return exchange.ExchangePrivate(ctx, nil, args, func(channel io.ReadWriteCloser) error {
		if err := lifecycle.WriteSourceTransferManifest(channel, manifest); err != nil {
			return err
		}
		return lifecycle.CheckpointSourceTransfer(ctx, channel, manifest.Operation, func(event lifecycle.SourceEvent) error {
			if finish {
				if event != (lifecycle.SourceEvent{Operation: manifest.Operation, Type: "source_finished"}) {
					return errComposeMigration
				}
				return nil
			}
			if event.Type == "archive_published" {
				if err := b.copyMigrationArchive(ctx, r, event); err != nil {
					return err
				}
			}
			return j.CheckpointMigrationSourceEvent(r.ID, event, time.Now())
		})
	})
}

// An interrupted private pipe can leave its Docker child alive. Inspect the
// original helper's whole finite mount boundary before removing that child;
// never stop an ordinary writer or adopt a container through labels alone.
func (b *Backend) cleanupMigrationSource(ctx context.Context, r lifecycle.OperationRecord, source string) error {
	name := b.migrationContainerName(r, "source")
	ids, err := b.runner.Run(ctx, nil, "container", "ls", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(ids)) == "" {
		return err
	}
	body, err := b.runner.Run(ctx, nil, "container", "inspect", name)
	if err != nil {
		return err
	}
	defer clear(body)
	var items []migrationContainer
	if len(strings.Fields(string(ids))) != 1 || json.Unmarshal(body, &items) != nil || len(items) != 1 {
		return errComposeMigration
	}
	c, i := items[0], r.SecretMigration.Intent
	l := c.Config.Labels
	if c.Name != "/"+name || l["io.loki.migration.owner"] != applicationOwnerID(b.root) || l["io.loki.migration.operation"] != r.ID || l["io.loki.migration.generation"] != i.CandidateGeneration || l["io.loki.migration.purpose"] != "source" || c.Config.Image != i.ContainerImage || c.Config.User != "0:0" || c.HostConfig.Privileged || !c.HostConfig.ReadonlyRootfs || c.HostConfig.NetworkMode != "none" || c.HostConfig.LogConfig.Type != "none" || !reflect.DeepEqual(c.Config.Entrypoint, []string{"/opt/loki/bin/loki"}) || !reflect.DeepEqual(c.Config.Cmd, []string{"host", "secret-migration-source"}) || len(c.Mounts) < 1 || len(c.Mounts) > 2 {
		return errComposeMigration
	}
	seen := map[string]bool{}
	for _, m := range c.Mounts {
		if seen[m.Destination] {
			return errComposeMigration
		}
		seen[m.Destination] = true
		switch m.Destination {
		case "/var/lib/loki/runtime":
			if m.Type != "volume" || m.Name != source || !m.RW {
				return errComposeMigration
			}
		case "/work":
			if m.Type != "volume" || m.Name != b.project+"-migration-"+r.ID+"-cipher" || !m.RW {
				return errComposeMigration
			}
		case "/run/loki/secrets/admin":
			if m.Type != "bind" || m.Source != filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-migration-"+r.ID, "secrets", "admin") || m.RW {
				return errComposeMigration
			}
		default:
			return errComposeMigration
		}
	}
	if !seen["/var/lib/loki/runtime"] {
		return errComposeMigration
	}
	_, err = b.runner.Run(ctx, nil, "container", "rm", "--force", c.ID)
	return err
}
