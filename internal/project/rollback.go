package project

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
)

// RollbackLegacy is offline and non-destructive: the candidate central state is
// retained privately, including changes made after migration, while originals
// return to .tasks. Interrupted restores continue without replacing user files.
func (s *Store) RollbackLegacy(ctx context.Context, cwd string) (map[string]any, error) {
	if s.Runner != "" && os.Geteuid() != 0 {
		return nil, fault.Error("managed legacy rollback requires root")
	}
	id, err := s.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	release, err := s.mutationLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rollbackRoot := filepath.Join(s.StateRoot, "rollback")
	if err = s.directory(rollbackRoot, 0700); err != nil {
		return nil, err
	}
	for _, candidate := range []string{id.StateDirectory, filepath.Join(filepath.Dir(id.StateDirectory), ".migrate-"+id.ProjectID)} {
		if _, err = os.Lstat(candidate); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		record, err := readMigration(candidate, id)
		if err != nil {
			return nil, fault.Error("central state has no matching legacy migration")
		}
		generation := record.Generation
		if generation == "" {
			generation = record.WorktreeID
		}
		backup := filepath.Join(rollbackRoot, id.ProjectID+"-"+generation)
		record.RollbackStarted = true
		if err = writeJSON(filepath.Join(candidate, "migration.json"), record, true); err != nil {
			return nil, err
		}
		if err = unix.Renameat2(unix.AT_FDCWD, candidate, unix.AT_FDCWD, backup, unix.RENAME_NOREPLACE); err != nil {
			return nil, err
		}
		if err = syncDirectory(filepath.Dir(candidate)); err != nil {
			return nil, err
		}
		if err = syncDirectory(rollbackRoot); err != nil {
			return nil, err
		}
		return finishLegacyRollback(id, backup, record)
	}
	// Find a durable interrupted rollback when the central directory has already
	// been moved. Completed generations allow idempotent operator retries.
	entries, err := os.ReadDir(rollbackRoot)
	if err != nil {
		return nil, err
	}
	if len(entries) > 10000 {
		return nil, fault.Error("rollback archive count exceeds limit")
	}
	var selected string
	var selectedRecord migrationRecord
	var newest time.Time
	active := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), id.ProjectID+"-") {
			continue
		}
		path := filepath.Join(rollbackRoot, entry.Name())
		record, err := readMigration(path, id)
		if err != nil || !record.RollbackStarted {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !record.RolledBack {
			active++
			selected = path
			selectedRecord = record
		} else if active == 0 && info.ModTime().After(newest) {
			newest = info.ModTime()
			selected = path
			selectedRecord = record
		}
	}
	if active > 1 {
		return nil, fault.Error("multiple interrupted legacy rollbacks require inspection")
	}
	if selected == "" {
		return nil, fault.Error("no matching legacy migration is available for rollback")
	}
	return finishLegacyRollback(id, selected, selectedRecord)
}

func finishLegacyRollback(id Identity, backup string, record migrationRecord) (map[string]any, error) {
	if !record.RolledBack {
		archive, err := migrationDirectory(filepath.Join(backup, "legacy"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if archive == nil {
			original, err := migrationDirectory(record.Source)
			if err != nil || record.Complete {
				if original != nil {
					original.Close()
				}
				return nil, fault.Error("legacy rollback original archive is missing")
			}
			original.Close()
		} else {
			defer archive.Close()
		}
		parent, err := migrationDirectory(filepath.Dir(record.Source))
		if err != nil {
			return nil, err
		}
		defer parent.Close()
		if err = unix.Mkdirat(int(parent.Fd()), ".tasks", 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		fd, err := unix.Openat(int(parent.Fd()), ".tasks", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		destination := os.NewFile(uintptr(fd), "restored legacy state")
		defer destination.Close()
		if archive != nil {
			entries, err := archive.ReadDir(10001)
			if err != nil && err != io.EOF {
				return nil, err
			}
			if len(entries) > 10000 {
				return nil, fault.Error("legacy rollback entry count exceeds limit")
			}
			for _, entry := range entries {
				var stat unix.Stat_t
				if err = unix.Fstatat(fd, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err == nil {
					return nil, fault.Error("legacy rollback destination already exists; candidate state remains in its rollback archive")
				}
				if !os.IsNotExist(err) {
					return nil, err
				}
			}
			for _, entry := range entries {
				if err = unix.Renameat2(int(archive.Fd()), entry.Name(), fd, entry.Name(), unix.RENAME_NOREPLACE); err != nil {
					return nil, err
				}
			}
			if err = archive.Sync(); err != nil {
				return nil, err
			}
		}
		if os.Geteuid() == 0 {
			if err = destination.Chown(record.UID, record.GID); err != nil {
				return nil, err
			}
		}
		if err = destination.Chmod(os.FileMode(record.Mode)); err != nil {
			return nil, err
		}
		if err = destination.Sync(); err != nil {
			return nil, err
		}
		if err = parent.Sync(); err != nil {
			return nil, err
		}
		record.RolledBack = true
		if err = writeJSON(filepath.Join(backup, "migration.json"), record, true); err != nil {
			return nil, err
		}
	}
	return map[string]any{"project_id": id.ProjectID, "rolled_back": true, "legacy_source": record.Source, "central_state_archive": backup}, nil
}
