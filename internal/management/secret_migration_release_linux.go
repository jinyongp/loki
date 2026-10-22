//go:build linux

package management

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"loki/internal/host/lifecycle"
)

// FenceSecretMigrationApplication controls only the original host operation's
// app write gate. End requires its durable terminal checkpoint; public app
// operations cannot supply or select this root-administrator channel.
func (b *DockerFullBackend) FenceSecretMigrationApplication(ctx context.Context, serving DeploymentReservation, begin bool) error {
	operation, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	if begin && operation.State != lifecycle.OperationApplying || !begin && operation.State != lifecycle.OperationSucceeded && operation.State != lifecycle.OperationRolledBack {
		return fmt.Errorf("application gate disposition differs from original host state")
	}
	record, err := b.record(serving)
	if err != nil || record.MigrationCandidate != nil {
		return fmt.Errorf("original serving app service is unavailable")
	}
	var root, layout, image string
	for _, spec := range record.Specs {
		if spec.Service != "secrets" {
			continue
		}
		image = spec.Image
		for _, mount := range spec.Mounts {
			if mount.Kind == "bind" && mount.Target == "/run/loki/secrets" {
				root = mount.Source
			}
			if mount.Kind == "bind" && mount.ReadOnly && mount.Target == "/etc/loki/secret-admin.json" {
				layout = mount.Source
			}
		}
	}
	if root == "" || layout == "" || image == "" {
		return fmt.Errorf("original app administrator projection is unavailable")
	}
	mounts := []FullMount{{Kind: "bind", Source: filepath.Join(root, "admin"), Target: "/run/loki/secrets/admin", ReadOnly: true}, {Kind: "bind", Source: layout, Target: "/etc/loki/secret-admin.json", ReadOnly: true}}
	for _, spec := range record.Specs {
		if spec.Service != "secrets" {
			continue
		}
		for _, mount := range spec.Mounts {
			if mount.Kind == "bind" && mount.ReadOnly && strings.HasPrefix(record.Worker, mount.Target+"/") {
				mounts = append(mounts, mount)
			}
		}
	}
	if len(mounts) != 3 {
		return fmt.Errorf("app gate helper lacks its immutable worker")
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--user", "0:0", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--memory", "134217728", "--pids-limit", "32", "--log-driver", "none", "--entrypoint", record.Worker}
	for _, mount := range mounts {
		value, err := dockerMountArgument(mount)
		if err != nil {
			return err
		}
		args = append(args, "--mount", value)
	}
	action := "end"
	if begin {
		action = "begin"
	}
	op := lifecycle.MigrationOperationUUID(operation.ID)
	args = append(args, image, "secret", "app", "maintenance", action, "--operation-id", op, "--layout", "/etc/loki/secret-admin.json")
	result, err := b.command(ctx, "confirming original application write gate", nil, args...)
	if err != nil {
		return err
	}
	var receipt struct {
		Maintenance struct {
			Version   int    `json:"version"`
			Operation string `json:"operation_id"`
			Active    bool   `json:"active"`
		} `json:"maintenance"`
	}
	if json.Unmarshal(result, &receipt) != nil || receipt.Maintenance.Version != 1 || receipt.Maintenance.Operation != op || receipt.Maintenance.Active != begin {
		return fmt.Errorf("original application gate was not acknowledged")
	}
	return nil
}

// FinalizeSecretMigration acknowledges actual source/app/Job disposition after
// the canonical result, then retires only proven absent reservations. A crash
// retains the same terminal operation until every original ACK is complete.
func (b *DockerFullBackend) FinalizeSecretMigration(ctx context.Context, candidate, serving DeploymentReservation) error {
	operation, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	if operation.State != lifecycle.OperationSucceeded && operation.State != lifecycle.OperationRolledBack {
		return fmt.Errorf("migration must have its original terminal result before release")
	}
	observation, err := b.Observe(ctx, serving)
	if err != nil || !observation.Ready {
		return fmt.Errorf("original serving health must recover before fence release")
	}
	if err := b.FinishSecretMigrationSource(ctx, candidate); err != nil {
		return err
	}
	if err := b.FenceSecretMigrationApplication(ctx, serving, false); err != nil {
		return err
	}
	servingRecord, err := b.record(serving)
	if err != nil {
		return err
	}
	if stringsIn(serving.Services, "launcher") {
		// The replacement launcher owns the SAME request ledger. Address its
		// live channel, rather than a stopped predecessor's offline helper.
		state, err := b.fullMaintenanceCall(ctx, servingRecord, "end", lifecycle.MigrationOperationUUID(operation.ID), false)
		if err != nil || state == nil || state.Active || state.OperationID != lifecycle.MigrationOperationUUID(operation.ID) || state.Interrupt != operation.Maintenance.Interrupt {
			return fmt.Errorf("actual serving Job fence release was not acknowledged")
		}
	}
	reservations, err := b.Store.Deployments()
	if err != nil {
		return err
	}
	for _, retired := range reservations {
		if retired.ID == serving.ID {
			continue
		}
		if err := b.Store.releaseDeploymentLocked(ctx, retired.ID, b); err != nil {
			return err
		}
	}
	scope := ctx.Value(fullMigrationMaintenanceKey{}).(*fullMigrationMaintenanceScope)
	_, err = scope.journal.AcknowledgeMigrationRelease(operation.ID, time.Now())
	return err
}
