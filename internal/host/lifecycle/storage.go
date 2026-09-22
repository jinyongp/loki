package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrLifecycleStorageQuotaExceeded = errors.New("host lifecycle retained storage quota exceeded")

type LifecycleStorageLimits struct {
	MaxBytes   int64
	MaxBackups int
	Retention  time.Duration
}

func DefaultLifecycleStorageLimits() LifecycleStorageLimits {
	return LifecycleStorageLimits{
		MaxBytes:   64 << 30,
		MaxBackups: 16,
		Retention:  30 * 24 * time.Hour,
	}
}

func normalizeLifecycleStorageLimits(limits LifecycleStorageLimits) (LifecycleStorageLimits, error) {
	defaults := DefaultLifecycleStorageLimits()
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxBackups == 0 {
		limits.MaxBackups = defaults.MaxBackups
	}
	if limits.Retention == 0 {
		limits.Retention = defaults.Retention
	}
	if limits.MaxBytes < 1 || limits.MaxBytes > 4<<40 ||
		limits.MaxBackups < 1 || limits.MaxBackups > 1024 ||
		limits.Retention < time.Millisecond || limits.Retention > 365*24*time.Hour {
		return LifecycleStorageLimits{}, errors.New("host lifecycle retained storage limits are outside the supported range")
	}
	return limits, nil
}

type RuntimeSnapshotInfo struct {
	Ref       string
	Bytes     int64
	CreatedAt time.Time
}

type RuntimeSnapshotStorage interface {
	RuntimeSnapshotUsage(context.Context, string) (int64, error)
	ListRuntimeSnapshots(context.Context) ([]RuntimeSnapshotInfo, error)
	DeleteRuntimeSnapshot(context.Context, string) error
}

type LifecycleStorageUsage struct {
	Bytes            int64
	BackupBytes      int64
	RuntimeBytes     int64
	Backups          int
	RuntimeSnapshots int
	ProtectedBackups int
	OrphanSnapshots  int
}

type LifecycleStorageGCResult struct {
	Before         LifecycleStorageUsage
	After          LifecycleStorageUsage
	RemovedBackups []string
	RemovedOrphans []string
	ReclaimedBytes int64
	Supported      bool
}

type lifecycleBackupStorageEntry struct {
	Backup    BackupRecord
	MetaBytes int64
	RunBytes  int64
	Protected bool
}

func (s *FileStore) normalizedStorageLimits() (LifecycleStorageLimits, error) {
	if s == nil {
		return LifecycleStorageLimits{}, errors.New("host lifecycle store is not configured")
	}
	return normalizeLifecycleStorageLimits(s.StorageLimits)
}

func (s *FileStore) backupPath(id string) (string, error) {
	if s == nil || !digestPattern.MatchString(id) {
		return "", errors.New("host lifecycle backup id is invalid")
	}
	return filepath.Join(s.path("backups"), strings.TrimPrefix(id, "sha256:")+".json"), nil
}

func (s *FileStore) backupMetadataBytes(id string) (int64, error) {
	path, err := s.backupPath(id)
	if err != nil {
		return 0, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return 0, errors.New("host lifecycle backup metadata must be a private regular file")
	}
	return info.Size(), nil
}

func (s *FileStore) DeleteBackup(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.backupPath(id)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	if directory, openErr := os.Open(dir); openErr == nil {
		defer directory.Close()
		return directory.Sync()
	} else if errors.Is(openErr, os.ErrNotExist) {
		return nil
	} else {
		return openErr
	}
}

func (e *TransactionEngine) CollectStorage(ctx context.Context) (LifecycleStorageGCResult, error) {
	if e == nil || e.Store == nil || e.Backend == nil {
		return LifecycleStorageGCResult{}, errors.New("host lifecycle transaction engine is not configured")
	}
	lock, journal, err := e.openJournal(ctx)
	if err != nil {
		return LifecycleStorageGCResult{}, err
	}
	defer lock.Close()
	return e.collectStorageLocked(ctx, journal, nil)
}

func (e *TransactionEngine) collectStorageLocked(
	ctx context.Context,
	journal *OperationJournal,
	extraProtected map[string]bool,
) (LifecycleStorageGCResult, error) {
	storage, ok := e.Backend.(RuntimeSnapshotStorage)
	if !ok {
		return LifecycleStorageGCResult{Supported: false}, nil
	}
	if err := ctx.Err(); err != nil {
		return LifecycleStorageGCResult{}, err
	}
	limits, err := e.Store.normalizedStorageLimits()
	if err != nil {
		return LifecycleStorageGCResult{}, err
	}
	backups, err := e.Store.ListBackups(ctx)
	if err != nil {
		return LifecycleStorageGCResult{}, err
	}
	snapshots, err := storage.ListRuntimeSnapshots(ctx)
	if err != nil {
		return LifecycleStorageGCResult{}, err
	}
	snapshotByRef := make(map[string]RuntimeSnapshotInfo, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.Ref == "" || snapshot.Bytes < 0 || snapshot.CreatedAt.IsZero() {
			return LifecycleStorageGCResult{}, errors.New("runtime snapshot storage returned invalid accounting")
		}
		if _, exists := snapshotByRef[snapshot.Ref]; exists {
			return LifecycleStorageGCResult{}, errors.New("runtime snapshot storage returned duplicate references")
		}
		snapshotByRef[snapshot.Ref] = snapshot
	}

	protected := map[string]bool{}
	for id := range extraProtected {
		if !digestPattern.MatchString(id) {
			return LifecycleStorageGCResult{}, errors.New("protected host lifecycle backup id is invalid")
		}
		protected[id] = true
	}
	records, err := journal.List()
	if err != nil {
		return LifecycleStorageGCResult{}, err
	}
	for _, record := range records {
		if record.RecoveryBackupID == "" {
			continue
		}
		if !record.State.Terminal() || record.State == OperationRecoveryFailed {
			protected[record.RecoveryBackupID] = true
		}
	}
	if rollbackID := newestRollbackBackup(backups); rollbackID != "" {
		protected[rollbackID] = true
	}

	referencedSnapshots := make(map[string]bool, len(backups))
	entries := make([]lifecycleBackupStorageEntry, 0, len(backups))
	var before LifecycleStorageUsage
	for _, backup := range backups {
		metaBytes, metaErr := e.Store.backupMetadataBytes(backup.ID)
		if metaErr != nil {
			return LifecycleStorageGCResult{}, metaErr
		}
		info, exists := snapshotByRef[backup.RuntimeRef]
		if !exists {
			return LifecycleStorageGCResult{}, fmt.Errorf("host lifecycle backup %s references missing runtime snapshot", backup.ID)
		}
		if referencedSnapshots[backup.RuntimeRef] {
			return LifecycleStorageGCResult{}, errors.New("host lifecycle backups share a runtime snapshot reference")
		}
		referencedSnapshots[backup.RuntimeRef] = true
		entry := lifecycleBackupStorageEntry{
			Backup: backup, MetaBytes: metaBytes, RunBytes: info.Bytes, Protected: protected[backup.ID],
		}
		entries = append(entries, entry)
		before.BackupBytes += metaBytes
		before.Backups++
		if entry.Protected {
			before.ProtectedBackups++
		}
	}
	for _, snapshot := range snapshots {
		before.RuntimeBytes += snapshot.Bytes
		before.RuntimeSnapshots++
		if !referencedSnapshots[snapshot.Ref] {
			before.OrphanSnapshots++
		}
	}
	before.Bytes = before.BackupBytes + before.RuntimeBytes
	result := LifecycleStorageGCResult{Before: before, Supported: true}

	// Orphans cannot be valid rollback or recovery roots because no durable
	// backup metadata points at them while the lifecycle operation lock is held.
	for _, snapshot := range snapshots {
		if referencedSnapshots[snapshot.Ref] {
			continue
		}
		if err = storage.DeleteRuntimeSnapshot(ctx, snapshot.Ref); err != nil {
			return result, err
		}
		result.RemovedOrphans = append(result.RemovedOrphans, snapshot.Ref)
		result.ReclaimedBytes += snapshot.Bytes
		before.RuntimeBytes -= snapshot.Bytes
		before.RuntimeSnapshots--
		before.OrphanSnapshots--
		before.Bytes -= snapshot.Bytes
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Backup.CreatedAt.Equal(entries[j].Backup.CreatedAt) {
			return entries[i].Backup.ID < entries[j].Backup.ID
		}
		return entries[i].Backup.CreatedAt.Before(entries[j].Backup.CreatedAt)
	})
	now := e.now()
	currentBytes := before.Bytes
	currentBackups := before.Backups
	for _, entry := range entries {
		if entry.Protected {
			continue
		}
		expired := !now.Before(entry.Backup.CreatedAt.Add(limits.Retention))
		overQuota := currentBytes > limits.MaxBytes || currentBackups > limits.MaxBackups
		if !expired && !overQuota {
			continue
		}
		// Remove the durable metadata first. A crash after this point leaves an
		// orphan snapshot, which the next collection can safely reclaim.
		if err = e.Store.DeleteBackup(ctx, entry.Backup.ID); err != nil {
			return result, err
		}
		currentBytes -= entry.MetaBytes
		currentBackups--
		result.ReclaimedBytes += entry.MetaBytes
		result.RemovedBackups = append(result.RemovedBackups, entry.Backup.ID)
		if err = storage.DeleteRuntimeSnapshot(ctx, entry.Backup.RuntimeRef); err != nil {
			return result, err
		}
		currentBytes -= entry.RunBytes
		result.ReclaimedBytes += entry.RunBytes
	}

	if currentBytes > limits.MaxBytes || currentBackups > limits.MaxBackups {
		return result, ErrLifecycleStorageQuotaExceeded
	}
	afterBackups, err := e.Store.ListBackups(ctx)
	if err != nil {
		return result, err
	}
	afterSnapshots, err := storage.ListRuntimeSnapshots(ctx)
	if err != nil {
		return result, err
	}
	var after LifecycleStorageUsage
	after.Backups = len(afterBackups)
	after.RuntimeSnapshots = len(afterSnapshots)
	afterProtected := map[string]bool{}
	for _, backup := range afterBackups {
		metaBytes, metaErr := e.Store.backupMetadataBytes(backup.ID)
		if metaErr != nil {
			return result, metaErr
		}
		after.BackupBytes += metaBytes
		afterProtected[backup.RuntimeRef] = true
		if protected[backup.ID] {
			after.ProtectedBackups++
		}
	}
	for _, snapshot := range afterSnapshots {
		after.RuntimeBytes += snapshot.Bytes
		if !afterProtected[snapshot.Ref] {
			after.OrphanSnapshots++
		}
	}
	after.Bytes = after.BackupBytes + after.RuntimeBytes
	result.After = after
	return result, nil
}

func newestRollbackBackup(backups []BackupRecord) string {
	var selected BackupRecord
	for _, backup := range backups {
		if backup.Installed == nil {
			continue
		}
		switch backup.Reason {
		case OperationApply, OperationInstall, OperationRestore, OperationRollback, OperationUninstall:
		default:
			continue
		}
		if selected.ID == "" || backup.CreatedAt.After(selected.CreatedAt) ||
			backup.CreatedAt.Equal(selected.CreatedAt) && backup.ID > selected.ID {
			selected = backup
		}
	}
	return selected.ID
}

func (e *TransactionEngine) discardBackup(ctx context.Context, backup BackupRecord) error {
	storage, ok := e.Backend.(RuntimeSnapshotStorage)
	if !ok {
		return e.Store.DeleteBackup(ctx, backup.ID)
	}
	metaErr := e.Store.DeleteBackup(ctx, backup.ID)
	snapshotErr := storage.DeleteRuntimeSnapshot(ctx, backup.RuntimeRef)
	return errors.Join(metaErr, snapshotErr)
}
