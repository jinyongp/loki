//go:build linux

package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"loki/internal/host/lifecycle"
)

func (b *Backend) migrationCandidateProtection(ctx context.Context, action string, input []byte) (string, error) {
	r, _, err := b.migrationRecord(ctx)
	if err != nil || r.SecretMigration.RollbackRequested || r.Phase != lifecycle.PhaseMigrate && !(action == "status" && r.Phase == lifecycle.PhaseSwitch) || action != "status" && action != "init" && action != "unlock" || (action != "status") != (len(input) > 0) || len(input) > 65536 {
		return "", errComposeMigration
	}
	private, ok := b.runner.(PrivateRunner)
	if !ok {
		return "", errComposeMigration
	}
	projection := filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-migration-"+r.ID, "secrets")
	generation, err := b.migrationGenerationSource(ctx, r)
	if err != nil {
		return "", err
	}
	c, found, err := b.inspectMigrationCandidate(ctx, r, projection, generation)
	if err != nil || !found || !c.State.Running {
		return "", errComposeMigration
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "134217728", "--pids-limit", "32", "--log-driver", "none", "--mount", applicationBind(filepath.Join(projection, "admin"), "/run/loki/secrets/admin", true), "--mount", applicationBind(filepath.Join(projection, "admin-layout.json"), "/etc/loki/secret-admin.json", true), "--entrypoint", "/opt/loki/bin/loki"}
	args = append(args, b.migrationLabels(r, "admin")...)
	if len(input) != 0 {
		args = append(args, "--interactive")
	}
	args = append(args, r.SecretMigration.Intent.ContainerImage, "secret", "protection", action, "--layout", "/etc/loki/secret-admin.json")
	if len(input) != 0 {
		args = append(args, "--stdin")
	}
	var body []byte
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	// Only finite public status is retried while the daemon starts its peers.
	// Protection input is sent exactly once per explicit operator invocation.
	for {
		body, err = private.RunPrivate(ctx, nil, bytes.NewReader(input), args...)
		if err == nil || action != "status" {
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return "", errComposeMigration
		case <-timer.C:
		}
	}
	defer clear(body)
	var result struct {
		Action string `json:"action"`
		State  string `json:"state"`
	}
	if err != nil || json.Unmarshal(body, &result) != nil || result.Action != action {
		return "", errComposeMigration
	}
	switch result.State {
	case "uninitialized", "unlocked", "locked", "locked_manual", "locking", "recovery_required":
		return result.State, nil
	default:
		return "", errComposeMigration
	}
}

// Remove only interrupted administrator helpers from this original operation.
// Candidate/source effects retain their own verified namespace and direction.
func (b *Backend) cleanupMigrationAdministrators(ctx context.Context) error {
	r, _, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	ids, err := b.runner.Run(ctx, nil, "container", "ls", "--all", "--filter", "label=io.loki.migration.owner="+applicationOwnerID(b.root), "--filter", "label=io.loki.migration.operation="+r.ID, "--filter", "label=io.loki.migration.purpose=admin", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(ids)) {
		body, err := b.runner.Run(ctx, nil, "container", "inspect", id)
		if err != nil {
			return err
		}
		var containers []migrationContainer
		if json.Unmarshal(body, &containers) != nil || len(containers) != 1 {
			clear(body)
			return errComposeMigration
		}
		clear(body)
		c := containers[0]
		labels := c.Config.Labels
		if labels["io.loki.migration.owner"] != applicationOwnerID(b.root) || labels["io.loki.migration.operation"] != r.ID || labels["io.loki.migration.purpose"] != "admin" || labels["io.loki.migration.generation"] != r.SecretMigration.Intent.CandidateGeneration || c.Config.Image != r.SecretMigration.Intent.ContainerImage || c.Config.User != "0:0" || c.HostConfig.Privileged || !c.HostConfig.ReadonlyRootfs || c.HostConfig.NetworkMode != "none" || c.HostConfig.LogConfig.Type != "none" || len(c.Mounts) != 2 || !reflect.DeepEqual(c.Config.Entrypoint, []string{"/opt/loki/bin/loki"}) || len(c.Config.Cmd) < 5 || c.Config.Cmd[0] != "secret" || c.Config.Cmd[1] != "protection" || c.Config.Cmd[2] != "status" && c.Config.Cmd[2] != "init" && c.Config.Cmd[2] != "unlock" || c.Config.Cmd[3] != "--layout" || c.Config.Cmd[4] != "/etc/loki/secret-admin.json" || len(c.Config.Cmd) != 5 && (len(c.Config.Cmd) != 6 || c.Config.Cmd[5] != "--stdin") {
			return errComposeMigration
		}
		projection := filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-migration-"+r.ID, "secrets")
		want := map[string]string{"/run/loki/secrets/admin": filepath.Join(projection, "admin"), "/etc/loki/secret-admin.json": filepath.Join(projection, "admin-layout.json")}
		for _, mount := range c.Mounts {
			if source, ok := want[mount.Destination]; !ok || mount.Type != "bind" || mount.Source != source || mount.RW {
				return errComposeMigration
			}
			delete(want, mount.Destination)
		}
		if _, err := b.runner.Run(ctx, nil, "container", "rm", "--force", c.ID); err != nil {
			return err
		}
	}
	return nil
}
