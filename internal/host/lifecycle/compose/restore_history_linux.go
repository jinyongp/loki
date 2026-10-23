//go:build linux

package compose

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

// Restore never replaces the current launcher journal. Only older spent
// requests are merged into it while its original maintenance gate and writer
// lock remain authoritative. Historical execution records stay in the stage.
func (b *Backend) restoreLauncherHistory(ctx context.Context, state runtimeState, directory, archive string) error {
	if b.maintenance == nil || !b.maintenance.Active || !operationUUID.MatchString(b.maintenance.OperationID) || state.ApplicationRuntimeRoot == "" {
		return errComposeMigration
	}
	private, ok := b.runner.(PrivateRunner)
	if !ok {
		return errComposeMigration
	}
	current, err := b.migrationVolume(ctx, "launcher-state")
	if err != nil {
		return err
	}
	op := b.maintenance.OperationID
	stage := b.project + "-restore-history-" + strings.ReplaceAll(op, "-", "")
	if _, err := b.runner.Run(ctx, nil, "volume", "create", "--label", "io.loki.restore.owner="+applicationOwnerID(b.root), "--label", "io.loki.restore.operation="+op, stage); err != nil {
		return err
	}
	body, err := b.runner.Run(ctx, nil, "volume", "inspect", stage)
	var volumes []struct {
		Name, Driver    string
		Labels, Options map[string]string
	}
	if err != nil || json.Unmarshal(body, &volumes) != nil || len(volumes) != 1 || volumes[0].Name != stage || volumes[0].Driver != "local" || len(volumes[0].Options) != 0 || volumes[0].Labels["io.loki.restore.owner"] != applicationOwnerID(b.root) || volumes[0].Labels["io.loki.restore.operation"] != op {
		return errComposeMigration
	}
	if err := b.restoreVolume(ctx, state, stage, directory, archive); err != nil {
		return err
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "268435456", "--pids-limit", "32", "--log-driver", "none", "--entrypoint", "/opt/loki/bin/loki", "--mount", "type=volume,source=" + current + ",target=/var/lib/loki/launcher,volume-nocopy", "--mount", "type=volume,source=" + stage + ",target=/restore-stage,volume-nocopy", "--mount", applicationBind(filepath.Join(state.ApplicationRuntimeRoot, "roles", "launcher.json"), "/etc/loki/launcher.json", true), state.CoreImage, "host", "maintenance", "preserve-history", "--offline", "--retain-current", "--operation-id", op, "--launcher-layout", "/etc/loki/launcher.json", "--state-mount", "/var/lib/loki/launcher", "--restore-stage", "/restore-stage"}
	body, err = private.RunPrivate(ctx, nil, nil, args...)
	var receipt struct {
		Operation string `json:"operation_id"`
		Preserved bool   `json:"preserved"`
	}
	if err != nil || json.Unmarshal(body, &receipt) != nil || receipt.Operation != op || !receipt.Preserved {
		return errComposeMigration
	}
	_, err = b.runner.Run(ctx, nil, "volume", "rm", stage)
	return err
}
