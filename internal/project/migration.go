package project

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/state"
)

type migrationRecord struct {
	Version    int    `json:"version"`
	WorktreeID string `json:"worktree_id"`
	Source     string `json:"source"`
	UID, GID   int
	Mode       uint32
	Retained   []string `json:"retained"`
	Complete   bool     `json:"complete"`
}

func readMigration(path string, id Identity) (migrationRecord, error) {
	var record migrationRecord
	raw, err := readFile(filepath.Join(path, "migration.json"), 65536)
	if err != nil {
		return record, err
	}
	if json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.WorktreeID != id.WorktreeID || record.Source != filepath.Join(id.WorktreeRoot, ".tasks") || record.UID < 0 || record.GID < 0 || record.Mode > 0777 {
		return record, fault.Error("legacy migration recovery record is invalid")
	}
	for _, name := range record.Retained {
		if filepath.Base(name) != name || name == "." || name == ".." || name == "data" || name == "items" || name == "taskrc" {
			return record, fault.Error("legacy migration retained entry is invalid")
		}
	}
	return record, nil
}

// MigrateLegacy is an offline administrator operation. Original files are moved
// into a protected rollback archive before copying, never recursively deleted.
// Interrupted staging is resumed using a durable record. The original and state
// roots must support an atomic rename; cross-filesystem moves fail unchanged.
func (s *Store) MigrateLegacy(ctx context.Context, cwd string) (map[string]any, error) {
	if s.Runner != "" && os.Geteuid() != 0 {
		return nil, fault.Error("managed legacy migration requires root")
	}
	id, err := s.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	release, err := s.mutationLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err = os.Lstat(id.StateDirectory); err == nil {
		record, err := readMigration(id.StateDirectory, id)
		if err != nil {
			return nil, fault.Error("central project state already exists")
		}
		return s.finishLegacyMigration(id, record)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	stage := filepath.Join(filepath.Dir(id.StateDirectory), ".migrate-"+id.ProjectID)
	source := filepath.Join(id.WorktreeRoot, ".tasks")
	var record migrationRecord
	if _, err = os.Lstat(stage); err == nil {
		record, err = readMigration(stage, id)
		if err != nil {
			return nil, err
		}
	} else if os.IsNotExist(err) {
		info, err := os.Lstat(source)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fault.Error("legacy .tasks directory is unsafe")
		}
		if _, err = migrationTree(ctx, source, ""); err != nil {
			return nil, err
		}
		prepared, err := os.MkdirTemp(filepath.Dir(stage), ".migration-prepare-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(prepared)
		stat := info.Sys().(*syscall.Stat_t)
		record = migrationRecord{Version: 1, WorktreeID: id.WorktreeID, Source: source, UID: int(stat.Uid), GID: int(stat.Gid), Mode: uint32(info.Mode().Perm()), Retained: []string{}}
		entries, err := os.ReadDir(source)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Name() != "data" && entry.Name() != "items" && entry.Name() != "taskrc" {
				record.Retained = append(record.Retained, entry.Name())
			}
		}
		if err = writeJSON(filepath.Join(prepared, "migration.json"), record, false); err != nil {
			return nil, err
		}
		if err = unix.Renameat2(unix.AT_FDCWD, prepared, unix.AT_FDCWD, stage, unix.RENAME_NOREPLACE); err != nil {
			return nil, err
		}
		if err = syncDirectory(filepath.Dir(stage)); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if err = os.Chmod(stage, 0700); err != nil {
		return nil, err
	}
	archive := filepath.Join(stage, "legacy")
	if _, err = os.Lstat(archive); os.IsNotExist(err) {
		parent, openErr := migrationDirectory(filepath.Dir(source))
		if openErr != nil {
			return nil, openErr
		}
		defer parent.Close()
		staging, openErr := migrationDirectory(stage)
		if openErr != nil {
			return nil, openErr
		}
		defer staging.Close()
		if err = unix.Renameat2(int(parent.Fd()), ".tasks", int(staging.Fd()), "legacy", unix.RENAME_NOREPLACE); err != nil {
			if errors.Is(err, unix.EXDEV) {
				return nil, fault.Error("legacy migration requires source and state on the same filesystem; original data remains in place")
			}
			return nil, err
		}
		if err = parent.Sync(); err != nil {
			return nil, err
		}
		if err = syncDirectory(stage); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err = checkParents(archive); err != nil {
		return nil, err
	}
	if os.Geteuid() == 0 {
		if err = os.Chown(archive, 0, -1); err != nil {
			return nil, err
		}
	}
	if err = os.Chmod(archive, 0700); err != nil {
		return nil, err
	}
	before, err := migrationTree(ctx, archive, "")
	if err != nil {
		return nil, err
	}
	// Capture retained names after the source has entered the private archive.
	record.Retained = []string{}
	archiveEntries, err := os.ReadDir(archive)
	if err != nil {
		return nil, err
	}
	for _, entry := range archiveEntries {
		if entry.Name() != "data" && entry.Name() != "items" && entry.Name() != "taskrc" {
			record.Retained = append(record.Retained, entry.Name())
		}
	}
	if err = writeJSON(filepath.Join(stage, "migration.json"), record, true); err != nil {
		return nil, err
	}
	// These are disposable generated copies under the private staging directory;
	// the original archive and recovery record are retained on every failure.
	for _, name := range []string{"taskwarrior", "workstreams", "bindings"} {
		path := filepath.Join(stage, name)
		if err = checkParents(path); err != nil {
			return nil, err
		}
		if err = os.RemoveAll(path); err != nil {
			return nil, err
		}
	}
	if err = os.Mkdir(filepath.Join(stage, "taskwarrior"), 0700); err != nil {
		return nil, err
	}
	for _, pair := range [][2]string{{"data", "taskwarrior/data"}, {"items", "workstreams"}} {
		from, to := filepath.Join(archive, pair[0]), filepath.Join(stage, pair[1])
		if _, err = os.Lstat(from); os.IsNotExist(err) {
			err = os.Mkdir(to, 0700)
		} else if err == nil {
			var copied, verified map[string]string
			copied, err = migrationTree(ctx, from, to)
			if err == nil {
				verified, err = migrationTree(ctx, to, "")
				if err == nil && !reflect.DeepEqual(copied, verified) {
					err = fault.Error("legacy migration copy verification failed")
				}
			}
		}
		if err != nil {
			return nil, err
		}
	}
	if err = os.Mkdir(filepath.Join(stage, "bindings"), 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(stage, "workstreams"))
	if err != nil {
		return nil, err
	}
	workstreams := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			workstreams = append(workstreams, entry.Name())
		}
	}
	if len(workstreams) == 1 {
		if err = writeJSON(filepath.Join(stage, "bindings", id.WorktreeID+".json"), map[string]string{"slug": workstreams[0]}, false); err != nil {
			return nil, err
		}
	}
	if err = state.AtomicWrite(filepath.Join(stage, "taskwarrior", "taskrc"), []byte("data.location="+filepath.Join(id.StateDirectory, "taskwarrior", "data")+"\nconfirmation=1\nhooks=0\n"), false); err != nil {
		return nil, err
	}
	if err = writeJSON(filepath.Join(stage, "project.json"), s.Metadata(id), true); err != nil {
		return nil, err
	}
	after, err := migrationTree(ctx, archive, "")
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(before, after) {
		return nil, fault.Error("legacy migration source changed; stop legacy writers before retrying")
	}
	if err = s.migrationPermissions(stage); err != nil {
		return nil, err
	}
	if err = syncDirectory(stage); err != nil {
		return nil, err
	}
	if err = unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, id.StateDirectory, unix.RENAME_NOREPLACE); err != nil {
		return nil, err
	}
	if err = syncDirectory(filepath.Dir(stage)); err != nil {
		return nil, err
	}
	return s.finishLegacyMigration(id, record)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *Store) migrationPermissions(stage string) error {
	if s.Runner == "" {
		return os.Chmod(filepath.Join(stage, "taskwarrior", "taskrc"), 0640)
	}
	account, err := user.Lookup(s.Runner)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	if s.GroupID < 0 {
		return fault.Error("managed migration requires a workspace group")
	}
	err = filepath.WalkDir(filepath.Join(stage, "taskwarrior", "data"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fault.Error("unsafe generated task data")
		}
		if err = os.Chown(path, uid, s.GroupID); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if entry.IsDir() {
			mode = 0700
		}
		return os.Chmod(path, mode)
	})
	if err != nil {
		return err
	}
	for _, path := range []string{stage, filepath.Join(stage, "taskwarrior"), filepath.Join(stage, "taskwarrior", "taskrc")} {
		if err = os.Chown(path, 0, s.GroupID); err != nil {
			return err
		}
		mode := os.FileMode(0710)
		if filepath.Base(path) == "taskrc" {
			mode = 0640
		}
		if err = os.Chmod(path, mode); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) finishLegacyMigration(id Identity, record migrationRecord) (map[string]any, error) {
	archive := filepath.Join(id.StateDirectory, "legacy")
	if !record.Complete {
		if len(record.Retained) > 0 {
			parent, err := migrationDirectory(filepath.Dir(record.Source))
			if err != nil {
				return nil, err
			}
			defer parent.Close()
			if err := unix.Mkdirat(int(parent.Fd()), ".tasks", 0700); err != nil && !os.IsExist(err) {
				return nil, err
			}
			fromDirectory, err := migrationDirectory(archive)
			if err != nil {
				return nil, err
			}
			defer fromDirectory.Close()
			destinationFD, err := unix.Openat(int(parent.Fd()), ".tasks", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return nil, err
			}
			toDirectory := os.NewFile(uintptr(destinationFD), "retained legacy output")
			defer toDirectory.Close()
			fromFD, toFD := int(fromDirectory.Fd()), int(toDirectory.Fd())
			for _, name := range record.Retained {
				var stat unix.Stat_t
				if err := unix.Fstatat(fromFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); os.IsNotExist(err) {
					if err = unix.Fstatat(toFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
						return nil, fault.Error("retained legacy output is missing")
					}
					continue
				} else if err != nil {
					return nil, err
				}
				if err := unix.Renameat2(fromFD, name, toFD, name, unix.RENAME_NOREPLACE); err != nil {
					return nil, err
				}
			}
			if os.Geteuid() == 0 {
				if err := toDirectory.Chown(record.UID, record.GID); err != nil {
					return nil, err
				}
			}
			if err := toDirectory.Chmod(os.FileMode(record.Mode)); err != nil {
				return nil, err
			}
			if err := toDirectory.Sync(); err != nil {
				return nil, err
			}
			if err := fromDirectory.Sync(); err != nil {
				return nil, err
			}
		}
		record.Complete = true
		if err := writeJSON(filepath.Join(id.StateDirectory, "migration.json"), record, true); err != nil {
			return nil, err
		}
	}
	retained := []string{}
	sourceDirectory, err := migrationDirectory(record.Source)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	removed := os.IsNotExist(err)
	var entries []os.DirEntry
	if sourceDirectory != nil {
		defer sourceDirectory.Close()
		entries, err = sourceDirectory.ReadDir(10001)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(entries) > 10000 {
			return nil, fault.Error("retained legacy output count exceeds limit")
		}
	}
	for _, entry := range entries {
		retained = append(retained, entry.Name())
	}
	sort.Strings(retained)
	workstreams, err := os.ReadDir(filepath.Join(id.StateDirectory, "workstreams"))
	if err != nil {
		return nil, err
	}
	result := s.Metadata(id)
	result["migrated"] = true
	result["legacy_removed"] = removed
	result["worktree_local_entries_retained"] = retained
	result["workstreams"] = len(workstreams)
	result["legacy_archive"] = archive
	return result, nil
}

func migrationDirectory(path string) (*os.File, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "migration directory"), nil
}
