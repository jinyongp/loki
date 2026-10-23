//go:build linux

package migration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/safeio"
)

// These are the installed suite units, including independently startable
// writers. A target-only condition would not fence a direct service restart.
var nativeMigrationUnits = []string{
	"loki-go.target", "loki-go-secrets-prepare.service", "loki-go-secrets.service",
	"loki-go-mcp.service", "loki-go-runtime.service", "loki-go-executor.service",
	"loki-go-launcher.service", "loki-go-egress-proxy.service", "loki-go-browser.service",
	"loki-go-browser-proxy.service", "loki-go-signing-agent.service", "loki-go-port-guard.service",
}

type nativeBootInterlock struct{ root, operation string }

func (g nativeBootInterlock) permit() string {
	return filepath.Join(g.root, "run/loki-go/secret-migration", g.operation, "allow")
}
func (g nativeBootInterlock) condition() []byte {
	// The permit is volatile. After ANY reboot the persistent condition blocks
	// ordinary services until the original journal owner prepares serving roles.
	return []byte("# Original LOKI secret migration " + g.operation + "\n[Unit]\nConditionPathExists=/run/loki-go/secret-migration/" + g.operation + "/allow\n")
}
func (g nativeBootInterlock) dropin(unit string) string {
	return filepath.Join(g.root, "etc/systemd/system", unit+".d", "70-secret-migration.conf")
}

func nativeOwnedDirectory(path string, create bool) error {
	created := false
	if create {
		if err := os.Mkdir(path, 0700); err == nil {
			created = true
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Gid != 0 || st.Mode&0022 != 0 {
		return ErrSourceSession
	}
	if created {
		if err := unix.Fsync(fd); err != nil {
			return err
		}
		return syncNativeDirectory(filepath.Dir(path))
	}
	return nil
}

func nativeInterlockFile(path string, want []byte, create bool) error {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if errors.Is(err, os.ErrNotExist) && create {
		return safeio.PublishPrivate(path, want, false)
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "native-migration-interlock")
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() != int64(len(want)) {
		return ErrSourceSession
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || owner.Gid != 0 || owner.Nlink != 1 {
		return ErrSourceSession
	}
	body := make([]byte, len(want))
	if _, err := f.ReadAt(body, 0); err != nil || !bytes.Equal(body, want) {
		return ErrSourceSession
	}
	return nil
}

func (g nativeBootInterlock) install() error {
	if os.Geteuid() != 0 || !filepath.IsAbs(g.root) || filepath.Clean(g.root) != g.root || lifecycle.MigrationOperationUUID(g.operation) == "" {
		return ErrSourceSession
	}
	if err := nativeOwnedDirectory(filepath.Join(g.root, "etc/systemd/system"), false); err != nil {
		return err
	}
	for _, unit := range nativeMigrationUnits {
		path := g.dropin(unit)
		if err := nativeOwnedDirectory(filepath.Dir(path), true); err != nil {
			return err
		}
		if err := nativeInterlockFile(path, g.condition(), true); err != nil {
			return err
		}
	}
	return nil
}

func (g nativeBootInterlock) allow() error {
	// Never grant a volatile permit without all persistent conditions intact.
	for _, unit := range nativeMigrationUnits {
		if err := nativeInterlockFile(g.dropin(unit), g.condition(), false); err != nil {
			return err
		}
	}
	for _, dir := range []string{"run/loki-go", "run/loki-go/secret-migration", "run/loki-go/secret-migration/" + g.operation} {
		if err := nativeOwnedDirectory(filepath.Join(g.root, dir), true); err != nil {
			return err
		}
	}
	return nativeInterlockFile(g.permit(), []byte(g.operation+"\n"), true)
}

func syncNativeDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (g nativeBootInterlock) remove() error {
	for _, unit := range nativeMigrationUnits {
		path := g.dropin(unit)
		if err := nativeInterlockFile(path, g.condition(), false); errors.Is(err, os.ErrNotExist) {
			if err := syncNativeDirectory(filepath.Dir(path)); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := syncNativeDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	return nil
}

func (o SourceOwner) nativeBootRecord() (lifecycle.OperationRecord, error) {
	if os.Geteuid() != 0 || o.Journal == nil {
		return lifecycle.OperationRecord{}, ErrSourceSession
	}
	r, found, err := o.Journal.Active()
	if err != nil || !found || r.ID != o.ID || r.SecretMigration == nil || r.SecretMigration.Intent.Mode != "native" {
		return lifecycle.OperationRecord{}, ErrSourceSession
	}
	return r, nil
}

// FenceNativeBoot precedes source freeze and any ordinary service stop. Files
// contain only the original operation reference; progress stays in its journal.
func (o SourceOwner) FenceNativeBoot(ctx context.Context) error {
	r, err := o.nativeBootRecord()
	if err != nil || r.State.Terminal() && r.State != lifecycle.OperationSucceeded && r.State != lifecycle.OperationRolledBack {
		return ErrSourceSession
	}
	if err := (nativeBootInterlock{root: "/", operation: r.ID}).install(); err != nil {
		return err
	}
	_, err = nativeCommand(ctx, "/usr/bin/systemctl", "daemon-reload")
	return err
}

// PermitNativeStart follows role/catalog preparation. A reboot invalidates the
// permit even while the original host operation still requires recovery.
func (o SourceOwner) PermitNativeStart() error {
	r, err := o.nativeBootRecord()
	if err != nil || !r.State.Terminal() && !r.SecretMigration.RollbackRequested && r.Phase != lifecycle.PhaseRestart && r.Phase != lifecycle.PhaseHealth {
		return ErrSourceSession
	}
	return (nativeBootInterlock{root: "/", operation: r.ID}).allow()
}

// ReleaseNativeBoot is separate from the host release ACK and requires both
// terminal health and actual app/Job release by the caller before invocation.
func (o SourceOwner) ReleaseNativeBoot(ctx context.Context) error {
	r, err := o.nativeBootRecord()
	if err != nil || r.State != lifecycle.OperationSucceeded && r.State != lifecycle.OperationRolledBack {
		return ErrSourceSession
	}
	if err := (nativeBootInterlock{root: "/", operation: r.ID}).remove(); err != nil {
		return err
	}
	_, err = nativeCommand(ctx, "/usr/bin/systemctl", "daemon-reload")
	return err
}
