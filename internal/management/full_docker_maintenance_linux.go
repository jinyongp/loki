package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"loki/internal/host/lifecycle"
)

// The existing deployment service record owns stop intent. Jobs, cancellation
// and replay authority stay in the launcher's canonical journal.
type fullMaintenanceIntent struct {
	OperationID string `json:"operation_id"`
	Interrupt   bool   `json:"interrupt"`
	Migration   bool   `json:"migration,omitempty"`
}

type fullMigrationMaintenanceKey struct{}
type fullMigrationMaintenanceScope struct {
	root    string
	journal *lifecycle.OperationJournal
	active  bool
}

// WithSecretMigrationMaintenance delegates service effects to the ORIGINAL
// transaction owner. The capability expires with this callback and the existing
// store lock; its UUID alone never authorizes a deployment stop or fence release.
func (b *DockerFullBackend) WithSecretMigrationMaintenance(ctx context.Context, journal *lifecycle.OperationJournal, use func(context.Context) error) error {
	if use == nil || journal == nil || ctx.Value(fullMigrationMaintenanceKey{}) != nil {
		return fmt.Errorf("original full migration owner is required")
	}
	scope := &fullMigrationMaintenanceScope{root: b.Store.Root, journal: journal, active: true}
	defer func() { scope.active = false }()
	ctx = context.WithValue(ctx, fullMigrationMaintenanceKey{}, scope)
	if _, err := b.migrationMaintenance(ctx); err != nil {
		return err
	}
	return use(ctx)
}

func (b *DockerFullBackend) migrationMaintenance(ctx context.Context) (*fullMaintenanceIntent, error) {
	t, err := b.Store.readTransaction()
	if err != nil {
		return nil, err
	}
	pending := t != nil && t.Action == "secret_migration" && t.SecretMigration != nil && !t.SecretMigration.Maintenance.Released
	scope, _ := ctx.Value(fullMigrationMaintenanceKey{}).(*fullMigrationMaintenanceScope)
	if scope == nil {
		if pending {
			return nil, fmt.Errorf("resume the original full secret migration before service effects")
		}
		return nil, nil
	}
	if !pending || !scope.active || scope.root != b.Store.Root {
		return nil, fmt.Errorf("full migration owner capability is unavailable")
	}
	owner, err := scope.journal.StoreIdentity()
	if err != nil || owner != filepath.Join(b.Store.Root, "transaction.json") {
		return nil, fmt.Errorf("full migration journal belongs to another store")
	}
	r, found, err := scope.journal.Active()
	if err != nil || !found || r.ID != t.SecretMigration.ID || r.PlanID != t.SecretMigration.PlanID || r.SecretMigration == nil || r.SecretMigration.Intent.Mode != "managed-full" {
		return nil, fmt.Errorf("full migration owner lock or original transaction changed")
	}
	return &fullMaintenanceIntent{OperationID: lifecycle.MigrationOperationUUID(r.ID), Interrupt: r.Maintenance.Interrupt, Migration: true}, nil
}

func (b *DockerFullBackend) validStopMaintenance(r DeploymentReservation, intent *fullMaintenanceIntent) bool {
	if intent == nil {
		return true
	}
	if !intent.Migration {
		return intent.OperationID == deploymentStopUUID(r)
	}
	t, err := b.Store.readTransaction()
	return err == nil && t != nil && t.Action == "secret_migration" && t.SecretMigration != nil && !t.SecretMigration.Maintenance.Released && intent.OperationID == lifecycle.MigrationOperationUUID(t.SecretMigration.ID) && intent.Interrupt == t.SecretMigration.Maintenance.Interrupt
}

type fullMaintenanceReceipt struct {
	Version     int    `json:"version"`
	OperationID string `json:"operation_id"`
	Interrupt   bool   `json:"interrupt"`
	Active      bool   `json:"active"`
}

func (b *DockerFullBackend) FenceJobs(ctx context.Context, r DeploymentReservation) error {
	return b.beginStopMaintenance(ctx, r)
}

func (b *DockerFullBackend) DeploymentStopping(r DeploymentReservation) (bool, error) {
	record, err := b.record(r)
	return record.StopMaintenance != nil, err
}

func deploymentStopUUID(r DeploymentReservation) string {
	id := strings.TrimPrefix(r.ID, "deployment-")
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}

func (b *DockerFullBackend) beginStopMaintenance(ctx context.Context, r DeploymentReservation) error {
	migration, err := b.migrationMaintenance(ctx)
	if err != nil {
		return err
	}
	if !stringsIn(r.Services, "launcher") {
		return nil
	}
	record, err := b.record(r)
	if err != nil {
		return err
	}
	if record.Preparing {
		ids, err := b.containerIDs(ctx, r)
		if err != nil {
			return err
		}
		for _, id := range ids {
			c, err := b.inspectContainer(ctx, id)
			if err != nil {
				return err
			}
			if err := b.requireParent(r, c); err != nil {
				return err
			}
			if c.Config.Labels[fullServiceLabel] != "prepare" {
				return fmt.Errorf("unfinished preparation has unexpected execution resources")
			}
		}
		return nil
	}
	interrupt, _ := ctx.Value(fullInterruptionKey{}).(bool)
	if record.StopMaintenance == nil {
		record.StopMaintenance = &fullMaintenanceIntent{OperationID: deploymentStopUUID(r), Interrupt: interrupt}
		if migration != nil {
			record.StopMaintenance = migration
		}
		directory, err := b.deploymentDirectory(r)
		if err != nil {
			return err
		}
		if err := atomicJSON(filepath.Join(directory, "services.json"), record); err != nil {
			return err
		}
	}
	intent := record.StopMaintenance
	if !b.validStopMaintenance(r, intent) || intent.Migration && (migration == nil || *intent != *migration) || !intent.Migration && migration != nil {
		return fmt.Errorf("deployment stop identity differs from its reservation")
	}
	state, err := b.fullMaintenanceCall(ctx, record, "status", "", false)
	if err != nil {
		return err
	}
	if state != nil && state.OperationID == intent.OperationID && !state.Active {
		return nil
	}
	state, err = b.fullMaintenanceCall(ctx, record, "begin", intent.OperationID, intent.Interrupt)
	if err != nil {
		// A default busy rejection admits no operation. Only a fresh observed
		// status can prove that changing the next request's choice is safe.
		if current, observed := b.fullMaintenanceCall(ctx, record, "status", "", false); observed == nil && (current == nil || !current.Active && current.OperationID != intent.OperationID) {
			record.StopMaintenance = nil
			if directory, pathErr := b.deploymentDirectory(r); pathErr == nil {
				_ = atomicJSON(filepath.Join(directory, "services.json"), record)
			}
		}
		return err
	}
	if state == nil || !state.Active || state.OperationID != intent.OperationID || state.Interrupt != intent.Interrupt {
		return fmt.Errorf("deployment stop admission fence was not confirmed")
	}
	return nil
}

func (b *DockerFullBackend) endStopMaintenance(ctx context.Context, r DeploymentReservation) error {
	migration, err := b.migrationMaintenance(ctx)
	if err != nil {
		return err
	}
	if !stringsIn(r.Services, "launcher") {
		return nil
	}
	record, err := b.record(r)
	if err != nil {
		return err
	}
	if record.Preparing {
		return nil
	}
	if record.StopMaintenance != nil && record.StopMaintenance.Migration {
		if migration == nil || *migration != *record.StopMaintenance {
			return fmt.Errorf("original migration owner must retain its Job fence")
		}
		// Stopping containers is not cutover/health completion. Keep this exact
		// fence until the canonical migration has a durable terminal result.
		return nil
	}
	if record.StopMaintenance == nil || record.StopMaintenance.OperationID != deploymentStopUUID(r) {
		return fmt.Errorf("deployment stop intent is unavailable")
	}
	state, err := b.fullMaintenanceCall(ctx, record, "end", record.StopMaintenance.OperationID, false)
	if err != nil {
		return err
	}
	if state == nil || state.Active || state.OperationID != record.StopMaintenance.OperationID {
		return fmt.Errorf("deployment stop fence release was not confirmed")
	}
	return nil
}

// ReleaseSecretMigrationMaintenance follows selector/restart/health and the
// canonical terminal checkpoint. It acknowledges the actual Job ledger before
// removing the service leaf intent; the caller then acknowledges host release.
func (b *DockerFullBackend) ReleaseSecretMigrationMaintenance(ctx context.Context, r DeploymentReservation) error {
	intent, err := b.migrationMaintenance(ctx)
	if err != nil || intent == nil {
		return fmt.Errorf("original migration owner is required for release")
	}
	scope := ctx.Value(fullMigrationMaintenanceKey{}).(*fullMigrationMaintenanceScope)
	current, found, err := scope.journal.Active()
	if err != nil || !found || !current.State.Terminal() {
		return fmt.Errorf("migration must be durably terminal before Job fence release")
	}
	record, err := b.record(r)
	if err != nil {
		return err
	}
	if record.StopMaintenance == nil {
		return nil
	}
	if *record.StopMaintenance != *intent {
		return fmt.Errorf("another host operation owns the deployment fence")
	}
	state, err := b.fullMaintenanceCall(ctx, record, "end", intent.OperationID, false)
	if err != nil {
		return err
	}
	if state == nil || state.Active || state.OperationID != intent.OperationID || state.Interrupt != intent.Interrupt {
		return fmt.Errorf("migration Job fence release was not confirmed")
	}
	record.StopMaintenance = nil
	directory, err := b.deploymentDirectory(r)
	if err != nil {
		return err
	}
	return atomicJSON(filepath.Join(directory, "services.json"), record)
}

func stringsIn(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func (b *DockerFullBackend) fullMaintenanceCall(ctx context.Context, record fullDockerRecord, action, operation string, interrupt bool) (*fullMaintenanceReceipt, error) {
	var launcher *fullContainerSpec
	for index := range record.Specs {
		if record.Specs[index].Service == "launcher" {
			launcher = &record.Specs[index]
			break
		}
	}
	if launcher == nil {
		return nil, fmt.Errorf("reserved launcher service is unavailable")
	}
	ids, err := b.containerIDs(ctx, record.Reservation)
	if err != nil {
		return nil, err
	}
	live := false
	for _, id := range ids {
		c, err := b.inspectContainer(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := b.requireParent(record.Reservation, c); err != nil {
			return nil, err
		}
		if c.Config.Labels[fullServiceLabel] == "launcher" {
			if err := b.verifySpec(record.Reservation, *launcher, c); err != nil {
				return nil, err
			}
			live = c.State.Running
		}
	}
	args := []string{"host", "maintenance", action, "--socket", "/run/loki/launcher/control.sock"}
	if operation != "" {
		args = append(args, "--operation-id", operation)
	}
	if interrupt {
		args = append(args, "--interrupt")
	}
	var command []string
	if live {
		command = append([]string{"container", "exec", "--user", "0:0", launcher.Name, record.Worker}, args...)
	} else {
		if action == "begin" {
			return nil, fmt.Errorf("recover the original launcher before interrupting retained Jobs")
		}
		command = []string{"run", "--rm", "--network", "none", "--read-only", "--user", "0:0", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--memory", "134217728", "--pids-limit", "32", "--entrypoint", record.Worker}
		launcherVolume := ""
		for _, mount := range launcher.Mounts {
			if mount.Kind == "volume" && mount.Target == "/var/lib/loki/launcher" {
				launcherVolume = mount.Source
			}
		}
		workerBound, layoutBound := false, false
		for _, mount := range launcher.Mounts {
			include := launcherVolume != "" && mount.Kind == "volume" && mount.Source == launcherVolume
			if mount.Kind == "bind" && mount.ReadOnly && (record.Worker == mount.Target || strings.HasPrefix(record.Worker, mount.Target+"/")) {
				include = true
				workerBound = true
			}
			if mount.Kind == "bind" && mount.ReadOnly && mount.Target == "/etc/loki/layout" {
				mount.Source = filepath.Join(mount.Source, "launcher.json")
				mount.Target = "/etc/loki/launcher.json"
				include = true
				layoutBound = true
			}
			if include {
				text, err := dockerMountArgument(mount)
				if err != nil {
					return nil, err
				}
				command = append(command, "--mount", text)
			}
		}
		if launcherVolume == "" || !workerBound || !layoutBound {
			return nil, fmt.Errorf("offline launcher journal projection is incomplete")
		}
		args = append(args, "--offline", "--launcher-layout", "/etc/loki/launcher.json", "--state-mount", "/var/lib/loki/launcher")
		command = append(command, launcher.Image)
		command = append(command, args...)
	}
	raw, err := b.command(ctx, "checking deployment Job maintenance", nil, command...)
	if err != nil {
		return nil, err
	}
	var result *fullMaintenanceReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&result) != nil || decoder.Decode(&extra) != io.EOF || result != nil && (result.Version != 1 || len(result.OperationID) != 36) {
		return nil, fmt.Errorf("invalid deployment Job maintenance response")
	}
	return result, nil
}
