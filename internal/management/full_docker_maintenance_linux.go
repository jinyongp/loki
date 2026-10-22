package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// The existing deployment service record owns stop intent. Jobs, cancellation
// and replay authority stay in the launcher's canonical journal.
type fullMaintenanceIntent struct {
	OperationID string `json:"operation_id"`
	Interrupt   bool   `json:"interrupt"`
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
		directory, err := b.deploymentDirectory(r)
		if err != nil {
			return err
		}
		if err := atomicJSON(filepath.Join(directory, "services.json"), record); err != nil {
			return err
		}
	}
	intent := record.StopMaintenance
	if intent.OperationID != deploymentStopUUID(r) {
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
