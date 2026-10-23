//go:build linux

package compose

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"loki/internal/host/lifecycle"
)

func (b *Backend) cleanupMigrationProjection(ctx context.Context, r lifecycle.OperationRecord) error {
	if !r.State.Terminal() {
		return errComposeMigration
	}
	parent := filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-migration-"+r.ID)
	var operator, st unix.Stat_t
	if err := unix.Lstat(parent, &st); err == unix.ENOENT {
		return nil
	} else if err != nil {
		return err
	}
	var fs unix.Statfs_t
	if unix.Lstat(b.root, &operator) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || st.Uid != operator.Uid || unix.Statfs(parent, &fs) != nil || fs.Type != unix.TMPFS_MAGIC {
		return errComposeMigration
	}
	projection := filepath.Join(parent, "secrets")
	if err := unix.Lstat(projection, &st); err == nil {
		if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || st.Uid != operator.Uid && st.Uid != 0 && st.Uid != 10006 {
			return errComposeMigration
		}
		private, ok := b.runner.(PrivateRunner)
		if !ok {
			return errComposeMigration
		}
		args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "134217728", "--pids-limit", "32", "--log-driver", "none", "--entrypoint", "/bin/sh", "--mount", applicationBind(projection, "/original-runtime", false), r.SecretMigration.Intent.ContainerImage, "-ec", "find /original-runtime -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +"}
		if _, err := private.RunPrivate(ctx, nil, nil, args...); err != nil {
			return err
		}
		if err := os.Remove(projection); err != nil {
			return err
		}
	} else if err != unix.ENOENT {
		return err
	}
	return os.Remove(parent)
}

func (b *Backend) cleanupMigrationCipher(ctx context.Context, r lifecycle.OperationRecord) error {
	if !r.State.Terminal() {
		return errComposeMigration
	}
	name := b.project + "-migration-" + r.ID + "-cipher"
	found, err := b.volumeExists(ctx, name)
	if err != nil || !found {
		return err
	}
	body, err := b.runner.Run(ctx, nil, "volume", "inspect", name)
	var items []struct {
		Name, Driver    string
		Labels, Options map[string]string
	}
	if err != nil || json.Unmarshal(body, &items) != nil || len(items) != 1 || items[0].Name != name || items[0].Driver != "local" || len(items[0].Options) != 0 || items[0].Labels["io.loki.migration.owner"] != applicationOwnerID(b.root) || items[0].Labels["io.loki.migration.operation"] != r.ID {
		return errComposeMigration
	}
	_, err = b.runner.Run(ctx, nil, "volume", "rm", name)
	return err
}
