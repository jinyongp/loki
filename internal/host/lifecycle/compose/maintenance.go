package compose

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"loki/internal/host/lifecycle"
)

type hostMaintenance struct {
	Version     int    `json:"version"`
	OperationID string `json:"operation_id"`
	Interrupt   bool   `json:"interrupt"`
	Active      bool   `json:"active"`
}

var _ lifecycle.TransactionMaintenance = (*Backend)(nil)
var operationUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (b *Backend) BeginJobMaintenance(ctx context.Context, operation string, interrupt bool) error {
	if !operationUUID.MatchString(operation) {
		return errors.New("invalid host operation UUID")
	}
	b.maintenance = &hostMaintenance{Version: 1, OperationID: operation, Interrupt: interrupt, Active: true}
	state, found, err := b.loadRuntime()
	if err != nil {
		return err
	}
	if !found {
		return nil
	} // The first launcher inherits this before listening.
	result, err := b.maintenanceCall(ctx, state, "begin", operation, interrupt, false)
	if err != nil {
		return err
	}
	if result == nil || !result.Active || result.OperationID != operation || result.Interrupt != interrupt {
		return errors.New("launcher admission fence was not confirmed")
	}
	return nil
}

func (b *Backend) EndJobMaintenance(ctx context.Context, operation string) error {
	if !operationUUID.MatchString(operation) {
		return errors.New("invalid host operation UUID")
	}
	state, found, err := b.loadRuntime()
	if err != nil {
		return err
	}
	if !found {
		b.maintenance = nil
		return nil
	}
	status, err := b.maintenanceCall(ctx, state, "status", "", false, true)
	if err != nil {
		return err
	}
	if status == nil || !status.Active && status.OperationID != operation {
		b.maintenance = nil
		return nil
	}
	if status.OperationID != operation {
		return errors.New("another host operation owns Job admission")
	}
	if status.Active && status.Interrupt {
		// A previous begin may have lost its response while cleaning Jobs. A
		// live launcher finishes that same operation before release. Offline
		// End independently refuses any retained unverified cleanup.
		live, err := b.launcherRunning(ctx, state)
		if err != nil {
			return err
		}
		if live {
			if _, err := b.maintenanceCall(ctx, state, "begin", operation, true, false); err != nil {
				return err
			}
		}
	}
	result, err := b.maintenanceCall(ctx, state, "end", operation, false, true)
	if err != nil {
		return err
	}
	if result == nil || result.Active || result.OperationID != operation {
		return errors.New("launcher admission release was not confirmed")
	}
	b.maintenance = nil
	return nil
}

func (b *Backend) launcherRunning(ctx context.Context, state runtimeState) (bool, error) {
	raw, err := b.compose(ctx, state, "ps", "--quiet", "--status", "running", "launcher")
	return len(strings.TrimSpace(string(raw))) != 0, err
}

func (b *Backend) maintenanceCall(ctx context.Context, state runtimeState, action, operation string, interrupt, allowOffline bool) (*hostMaintenance, error) {
	live, err := b.launcherRunning(ctx, state)
	if err != nil {
		return nil, err
	}
	args := []string{"host", "maintenance", action, "--socket", "/run/loki/launcher/control.sock"}
	if operation != "" {
		args = append(args, "--operation-id", operation)
	}
	if interrupt {
		args = append(args, "--interrupt")
	}
	var raw []byte
	if live {
		command := append([]string{"exec", "--no-TTY", "--user", "0:0", "launcher", "/opt/loki/bin/loki"}, args...)
		raw, err = b.compose(ctx, state, command...)
	} else {
		if !allowOffline || action == "begin" {
			return nil, errors.New("launcher must be recovered before host mutation")
		}
		args = append(args, "--offline", "--launcher-layout", "/etc/loki/launcher.json")
		layout := filepath.Join(state.ApplicationRuntimeRoot, "roles", "launcher.json")
		if state.ApplicationRuntimeRoot == "" {
			return nil, errors.New("frozen launcher layout is unavailable for offline recovery")
		}
		command := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--memory", "134217728", "--pids-limit", "32", "--user", "0:0", "--entrypoint", "/opt/loki/bin/loki", "--mount", "type=volume,source=" + b.volumeName("launcher-state") + ",target=/var/lib/loki/launcher,volume-nocopy", "--mount", applicationBind(layout, "/etc/loki/launcher.json", true), state.CoreImage}
		command = append(command, args...)
		raw, err = b.runner.Run(ctx, nil, command...)
	}
	if err != nil {
		return nil, err
	}
	var receipt *hostMaintenance
	if json.Unmarshal(raw, &receipt) != nil || receipt != nil && (receipt.Version != 1 || !operationUUID.MatchString(receipt.OperationID)) {
		return nil, errors.New("invalid host maintenance receipt")
	}
	return receipt, nil
}
