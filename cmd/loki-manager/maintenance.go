package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"loki/internal/management"
	"loki/internal/tools"
)

type idleMaintenanceBackend interface{ RequireIdle(context.Context) error }

func runMaintenance(ctx context.Context, store management.Store, args []string, input io.Reader, out, diagnostics io.Writer) (retErr error) {
	if len(args) == 0 {
		return fmt.Errorf("choose backup, backups, restore, rollback or uninstall")
	}
	action := args[0]
	purge := action == "uninstall" && len(args) == 2 && args[1] == "--purge-data"
	if (action == "backup" || action == "rollback" || action == "uninstall") && len(args) != 1 && !purge {
		return fmt.Errorf("usage: loki %s", action)
	}
	if action == "restore" && len(args) != 2 {
		return fmt.Errorf("usage: loki restore BACKUP-ID")
	}
	if action == "backups" && !(len(args) == 1 || len(args) == 2 && args[1] == "list" || len(args) == 3 && args[1] == "remove") {
		return fmt.Errorf("usage: loki backups list | loki backups remove BACKUP-ID")
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	if action == "restore" {
		if _, err := store.ReadBackup(args[1]); err != nil {
			return err
		}
		if _, err := store.RestorePending(args[1]); err != nil {
			return err
		}
	}
	if state.Config.Mode == tools.Full {
		if handled, err := privilegedMaintenance(ctx, store, args, input, out, diagnostics); handled {
			return err
		}
	}
	if action == "backups" {
		if len(args) == 3 {
			if err := store.RemoveBackup(args[2]); err != nil {
				return err
			}
			return success(out, "Backup removed.", map[string]any{"backup": args[2]})
		}
		records, err := store.Backups()
		if err != nil {
			return err
		}
		items := []map[string]any{}
		for _, record := range records {
			items = append(items, publicBackup(record))
		}
		return result(out, "Loki backups", map[string]any{"backups": items})
	}
	var backend management.FullBackend
	var data management.RestoreDataBackupBackend
	wasRunning := false
	if state.Config.Mode == tools.Full {
		backend, err = management.NewFullBackend(store, diagnostics)
		if err != nil {
			return err
		}
		idle, ok := backend.(idleMaintenanceBackend)
		if !ok {
			return fmt.Errorf("host does not provide safe maintenance readiness")
		}
		if err := idle.RequireIdle(ctx); err != nil {
			return err
		}
		data, ok = backend.(management.RestoreDataBackupBackend)
		if !ok {
			return fmt.Errorf("host does not provide owned data backups")
		}
		deployments, err := store.Deployments()
		if err != nil {
			return err
		}
		wasRunning = len(deployments) != 0
		if wasRunning {
			fmt.Fprintln(diagnostics, "Stopping owned services for consistent maintenance...")
			if err := store.StopFull(ctx, backend); err != nil {
				return err
			}
		}
	}
	// Restart the same desired services after a backup, including failed copies.
	// Restore/rollback restart the resulting desired selection only on success.
	if action == "backup" && wasRunning {
		defer func() {
			if !wasRunning {
				return
			}
			if _, err := store.ReconcileFull(ctx, backend); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("backup services could not resume; run loki tools start: %w", err))
			}
		}()
	}
	if (action == "restore" || action == "rollback") && wasRunning {
		defer func() {
			if retErr == nil {
				return
			}
			if action == "restore" {
				pending, err := store.RestorePending(args[1])
				if err != nil || pending {
					return
				}
			}
			if _, err := store.ReconcileFull(ctx, backend); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("services need recovery before loki tools start: %w", err))
			}
		}()
	}
	switch action {
	case "backup":
		fmt.Fprintln(diagnostics, "Backing up selected programs, settings and owned data...")
		record, err := store.Backup(ctx, data)
		if err != nil {
			return err
		}
		if wasRunning {
			wasRunning = false
			if _, err := store.ReconcileFull(ctx, backend); err != nil {
				return fmt.Errorf("backup %s created but services could not resume; run loki tools start: %w", record.ID, err)
			}
		}
		return result(out, "Loki backup", publicBackup(record))
	case "restore":
		// A fresh restore first retains current data. Resume uses its protected
		// journal and the already retained safety backup.
		resume, err := store.RestorePending(args[1])
		if err != nil {
			return err
		}
		if !resume {
			if _, err := store.ReadBackup(args[1]); err != nil {
				return err
			}
			record, err := store.Backup(ctx, data)
			if err != nil {
				return err
			}
			fmt.Fprintln(diagnostics, "Current data retained in safety backup:", record.ID)
		}
		fmt.Fprintln(diagnostics, "Restoring the selected backup...")
		if err := store.RestoreBackup(ctx, args[1], data); err != nil {
			return err
		}
	case "rollback":
		if err := store.RollbackTools(ctx); err != nil {
			return err
		}
	case "uninstall":
		if err := store.UninstallTools(ctx); err != nil {
			return err
		}
		if purge {
			if backend != nil {
				owned, ok := backend.(interface{ PurgeOwnedData(context.Context) error })
				if !ok {
					return fmt.Errorf("host does not provide owned data removal")
				}
				if err := owned.PurgeOwnedData(ctx); err != nil {
					return err
				}
			}
			if err := store.PurgeData(ctx); err != nil {
				return err
			}
			return success(out, "Tool programs, data and integration credentials removed. The CLI, host and backups are retained.", map[string]any{"tools_removed": true, "data_retained": false, "backups_retained": true})
		}
		return success(out, "Tool programs removed. User data, credentials and the CLI are retained.", map[string]any{"tools_removed": true, "data_retained": true})
	default:
		return fmt.Errorf("unknown maintenance action")
	}
	if wasRunning {
		next, err := store.Load()
		if err != nil {
			return err
		}
		if next.Config.Mode == tools.Full {
			fmt.Fprintln(diagnostics, "Starting the restored selection...")
			if _, err := store.ReconcileFull(ctx, backend); err != nil {
				return fmt.Errorf("%s completed but services need loki tools start: %w", action, err)
			}
		}
	}
	return success(out, "Loki "+action+" completed.", map[string]any{"action": action, "state": "completed"})
}

func publicBackup(record management.BackupRecord) map[string]any {
	return map[string]any{"id": record.ID, "created_at": record.CreatedAt, "version": record.Snapshot.Config.Release, "mode": record.Snapshot.Config.Mode, "tools": len(record.Snapshot.Installed), "data_volumes": len(record.Volumes)}
}
