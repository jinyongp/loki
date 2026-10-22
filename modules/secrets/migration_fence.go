package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/platform/safeio"
	"loki/internal/state"
)

const applicationMigrationFenceFile = "application-migration.json"

var ErrApplicationMigration = fault.New(fault.CodeUnavailable,
	"application vault is fenced for host migration", false,
	"resume the original host migration or use the activated application resource")

// ApplicationMigrationFence is a write gate tied to the canonical host
// operation, not an execution journal. Committing it permanently closes old
// application writes while restoring the existing platform owner's access.
type ApplicationMigrationFence struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	Revision  uint64 `json:"source_revision"`
	State     string `json:"state"`
}

func (f ApplicationMigrationFence) valid() bool {
	return f.Version == 1 && requestIDPattern.MatchString(f.Operation) && f.Revision > 0 && (f.State == "pending" || f.State == "committed")
}

func (c Controller) readApplicationMigrationFence() (*ApplicationMigrationFence, error) {
	fd, err := unix.Open(filepath.Join(c.StateDirectory, applicationMigrationFenceFile), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrApplicationMigration
	}
	f := os.NewFile(uintptr(fd), "application-migration-fence")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, ErrApplicationMigration
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return nil, ErrApplicationMigration
	}
	body, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(body) > 4096 {
		return nil, ErrApplicationMigration
	}
	var fence ApplicationMigrationFence
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&fence) != nil || !fence.valid() {
		return nil, ErrApplicationMigration
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return nil, ErrApplicationMigration
	}
	return &fence, nil
}

func (c Controller) checkApplicationMigrationWrite(managed bool) error {
	fence, err := c.readApplicationMigrationFence()
	if err != nil {
		return err
	}
	if fence != nil && (!managed || fence.State != "committed") {
		return ErrApplicationMigration
	}
	return nil
}

func (c Controller) publishApplicationMigrationFence(fence ApplicationMigrationFence) error {
	if !fence.valid() {
		return ErrApplicationMigration
	}
	body, _ := json.Marshal(fence)
	path := filepath.Join(c.StateDirectory, applicationMigrationFenceFile)
	if err := safeio.PublishPrivate(path, body, true); err != nil {
		return err
	}
	// The existing trusted store owner must be able to enforce the gate even
	// when the host administrator has a different UID. Agent/workload mounts
	// do not expose this private source directory.
	info, err := os.Stat(c.StateDirectory)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrApplicationMigration
	}
	return os.Chown(path, int(st.Uid), int(st.Gid))
}

// BeginApplicationMigration freezes all cooperating source writers at exactly
// the prepared revision. Call only after admitting the original host operation
// and its Job maintenance fence. Source ciphertext and key remain unchanged.
func (c Controller) BeginApplicationMigration(ctx context.Context, revision uint64, operation string) error {
	want := ApplicationMigrationFence{Version: 1, Operation: operation, Revision: revision, State: "pending"}
	if !want.valid() {
		return ErrApplicationMigration
	}
	return c.backend().WithSnapshot(ctx, &revision, func(state.Snapshot) error {
		previous, err := c.readApplicationMigrationFence()
		if err != nil {
			return err
		}
		if previous != nil {
			if previous.Operation != operation || previous.Revision != revision || previous.State != "pending" {
				return ErrApplicationMigration
			}
			return nil
		}
		return c.publishApplicationMigrationFence(want)
	})
}

// FinishApplicationMigration is invoked by canonical host recovery only.
// Commit follows the selector/policy checkpoint; rollback follows verified
// restoration. A committed source cannot be reopened by a rollback retry.
func (c Controller) FinishApplicationMigration(ctx context.Context, revision uint64, operation string, committed bool) error {
	if revision == 0 || !requestIDPattern.MatchString(operation) {
		return ErrApplicationMigration
	}
	return c.backend().WithSnapshot(ctx, nil, func(snapshot state.Snapshot) error {
		fence, err := c.readApplicationMigrationFence()
		if err != nil {
			return err
		}
		if fence == nil {
			if committed {
				return ErrApplicationMigration
			}
			return nil
		}
		if fence.Operation != operation || fence.Revision != revision {
			return ErrApplicationMigration
		}
		if fence.State == "committed" {
			if committed {
				return nil
			}
			return ErrApplicationMigration
		}
		if snapshot.Revision != revision {
			return state.ErrConflict
		}
		if committed {
			fence.State = "committed"
			return c.publishApplicationMigrationFence(*fence)
		}
		if err := os.Remove(filepath.Join(c.StateDirectory, applicationMigrationFenceFile)); err != nil {
			return err
		}
		dir, err := os.Open(c.StateDirectory)
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	})
}
