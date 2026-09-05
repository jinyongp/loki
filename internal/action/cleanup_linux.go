package action

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
)

const unsafeClear = fault.Error("materialized secret target is unsafe to clear")
const retainedRecovery = fault.Error("materialized secret cleanup requires recovery; file retained")

func overlaps(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
}

func validateMaterializationPaths(layout Layout) error {
	for _, private := range []string{layout.MaterializationDirectory, layout.MaterializationRecoveryDirectory} {
		if !filepath.IsAbs(private) || filepath.Clean(private) != private || private == "/" {
			return errors.New("materialization private directories must be configured")
		}
		sources := []string{layout.Workspace, "/usr", layout.SnapshotDirectory}
		for _, mount := range layout.PublicMounts {
			sources = append(sources, mount.Source)
		}
		for _, source := range sources {
			if source == "" {
				continue
			}
			resolved, err := filepath.EvalSymlinks(source)
			if err != nil {
				return err
			}
			if overlaps(private, resolved) {
				return errors.New("materialization private directory overlaps an action mount")
			}
		}
	}
	if overlaps(layout.MaterializationDirectory, layout.MaterializationRecoveryDirectory) {
		return errors.New("materialization journal and recovery directories must be separate")
	}
	return nil
}

func validateRecoveryDirectory(recovery, workspace *os.File, uid uint32) error {
	var stat, root unix.Stat_t
	if err := unix.Fstat(int(recovery.Fd()), &stat); err != nil {
		return err
	}
	if err := unix.Fstat(int(workspace.Fd()), &root); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0777 != 0700 || stat.Uid != uid || stat.Dev != root.Dev || stat.Ino == root.Ino {
		return errors.New("materialization recovery directory must be private, runner-owned, and on the workspace filesystem")
	}
	return nil
}

func pinRecoveryDirectory(layout Layout, workspace *os.File) (*os.File, error) {
	if err := validateMaterializationPaths(layout); err != nil {
		return nil, err
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, layout.MaterializationRecoveryDirectory, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "private materialization recovery")
	if err := validateRecoveryDirectory(file, workspace, layout.UID); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func sameFile(stat unix.Stat_t, record *materializationRecord) bool {
	return uint64(stat.Dev) == record.Device && stat.Ino == record.Inode
}

func clearable(stat unix.Stat_t, record *materializationRecord) bool {
	if !sameFile(stat, record) || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return false
	}
	if record.Explicit {
		return stat.Mode&0077 == 0 && (stat.Uid == 0 || stat.Uid == uint32(os.Geteuid()))
	}
	return stat.Mode&0777 == 0600 && stat.Size == 0 && stat.Uid == uint32(os.Geteuid())
}

func cleanupParent(workspace *os.File, target string) (*os.File, error) {
	parent, err := openWorkspaceFile(workspace, path.Dir(target), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, unsafeClear
	}
	return parent, nil
}

func cleanupIntent(parent, recovery, marker *os.File, record *materializationRecord) error {
	var target, parentStat, recoveryStat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), path.Base(record.Target), &target, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !clearable(target, record) {
		return unsafeClear
	}
	if err := unix.Fstat(int(parent.Fd()), &parentStat); err != nil {
		return err
	}
	if err := unix.Fstat(int(recovery.Fd()), &recoveryStat); err != nil {
		return err
	}
	if parentStat.Dev != recoveryStat.Dev {
		return errors.New("materialization recovery requires the target filesystem")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	record.Version = 2
	record.Quarantine = hex.EncodeToString(nonce[:])
	record.Phase = "capture"
	record.ParentDevice, record.ParentInode = uint64(parentStat.Dev), parentStat.Ino
	record.RecoveryDevice, record.RecoveryInode = uint64(recoveryStat.Dev), recoveryStat.Ino
	return writeMaterializationRecord(marker, record)
}

func syncCleanupDirectories(parent, recovery *os.File) error {
	if err := recovery.Sync(); err != nil {
		return err
	}
	return parent.Sync()
}

// The workspace basename is only renamed, never unlinked. An unexpected inode
// captured in the check/rename interval is restored without replacement.
func captureMaterialization(parent, recovery, marker *os.File, record *materializationRecord) error {
	if err := unix.Renameat2(int(parent.Fd()), path.Base(record.Target), int(recovery.Fd()), record.Quarantine, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	if err := syncCleanupDirectories(parent, recovery); err != nil {
		return err
	}
	record.Phase = "captured"
	return writeMaterializationRecord(marker, record)
}

func resetCleanupIntent(marker *os.File, record *materializationRecord) error {
	record.Quarantine, record.Phase, record.Explicit = "", "", false
	record.ParentDevice, record.ParentInode = 0, 0
	record.RecoveryDevice, record.RecoveryInode = 0, 0
	return writeMaterializationRecord(marker, record)
}

func restoreMaterialization(parent, recovery, marker *os.File, record *materializationRecord) error {
	if record.Phase != "restore" {
		record.Phase = "restore"
		if err := writeMaterializationRecord(marker, record); err != nil {
			return err
		}
	}
	err := unix.Renameat2(int(recovery.Fd()), record.Quarantine, int(parent.Fd()), path.Base(record.Target), unix.RENAME_NOREPLACE)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return retainedRecovery
	}
	if err := syncCleanupDirectories(parent, recovery); err != nil {
		return err
	}
	if err := resetCleanupIntent(marker, record); err != nil {
		return err
	}
	return unsafeClear
}

func finishMaterialization(parent, recovery, marker *os.File, record *materializationRecord) (bool, error) {
	if record.Phase == "restore" {
		return false, restoreMaterialization(parent, recovery, marker, record)
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(recovery.Fd()), record.Quarantine, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	if !clearable(stat, record) {
		return false, restoreMaterialization(parent, recovery, marker, record)
	}
	if !record.Explicit {
		// A read lease refuses an already-open writer or writable mapping and
		// blocks new writes until this finite cleanup attempt has ended.
		fd, err := unix.Openat(int(recovery.Fd()), record.Quarantine, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return false, restoreMaterialization(parent, recovery, marker, record)
		}
		file := os.NewFile(uintptr(fd), "captured placeholder")
		defer file.Close()
		notifications := make(chan os.Signal, 1)
		signal.Notify(notifications, syscall.SIGIO)
		defer signal.Stop(notifications)
		if _, err := unix.FcntlInt(file.Fd(), unix.F_SETLEASE, unix.F_RDLCK); err != nil {
			return false, restoreMaterialization(parent, recovery, marker, record)
		}
		defer unix.FcntlInt(file.Fd(), unix.F_SETLEASE, unix.F_UNLCK)
		if err := unix.Fstat(fd, &stat); err != nil || !clearable(stat, record) {
			return false, restoreMaterialization(parent, recovery, marker, record)
		}
		if lease, err := unix.FcntlInt(file.Fd(), unix.F_GETLEASE, 0); err != nil || lease != unix.F_RDLCK {
			return false, restoreMaterialization(parent, recovery, marker, record)
		}
	}
	// This basename is in the private, unmounted recovery directory. Action
	// processes cannot replace it. Existing writable descriptors were rejected.
	if err := unix.Unlinkat(int(recovery.Fd()), record.Quarantine, 0); err != nil {
		return false, err
	}
	if err := recovery.Sync(); err != nil {
		return false, err
	}
	return true, writeMaterializationRecord(marker, nil)
}

func resumeMaterialization(workspace, recovery, marker *os.File, record *materializationRecord) (bool, error) {
	var recoveryStat unix.Stat_t
	if err := unix.Fstat(int(recovery.Fd()), &recoveryStat); err != nil || uint64(recoveryStat.Dev) != record.RecoveryDevice || recoveryStat.Ino != record.RecoveryInode {
		return false, retainedRecovery
	}
	var captured unix.Stat_t
	err := unix.Fstatat(int(recovery.Fd()), record.Quarantine, &captured, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return false, err
	}
	if errors.Is(err, unix.ENOENT) && record.Phase == "captured" {
		return true, writeMaterializationRecord(marker, nil)
	}
	parent, parentErr := cleanupParent(workspace, record.Target)
	if parentErr != nil {
		return false, retainedRecovery
	}
	defer parent.Close()
	var parentStat unix.Stat_t
	if e := unix.Fstat(int(parent.Fd()), &parentStat); e != nil || uint64(parentStat.Dev) != record.ParentDevice || parentStat.Ino != record.ParentInode {
		return false, retainedRecovery
	}
	if record.Phase == "restore" {
		return false, restoreMaterialization(parent, recovery, marker, record)
	}
	if errors.Is(err, unix.ENOENT) {
		var current unix.Stat_t
		err := unix.Fstatat(int(parent.Fd()), path.Base(record.Target), &current, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			return false, writeMaterializationRecord(marker, nil)
		}
		if err != nil || !clearable(current, record) {
			return false, unsafeClear
		}
		if err := captureMaterialization(parent, recovery, marker, record); err != nil {
			return false, err
		}
	} else if record.Phase == "capture" {
		// A crash may occur after rename but before recording its completion.
		if err := syncCleanupDirectories(parent, recovery); err != nil {
			return false, err
		}
		record.Phase = "captured"
		if err := writeMaterializationRecord(marker, record); err != nil {
			return false, err
		}
	}
	return finishMaterialization(parent, recovery, marker, record)
}

func clearMaterialization(workspace, marker, recovery *os.File, target string) (bool, error) {
	record, err := readMaterializationRecord(marker)
	if err != nil || record == nil {
		return false, err
	}
	if record.Target != target {
		return false, fault.Error("materialized secret marker target mismatch")
	}
	if record.Quarantine != "" {
		return resumeMaterialization(workspace, recovery, marker, record)
	}
	parent, err := cleanupParent(workspace, target)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	if err := cleanupIntent(parent, recovery, marker, record); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, writeMaterializationRecord(marker, nil)
		}
		return false, err
	}
	if err := captureMaterialization(parent, recovery, marker, record); err != nil {
		return false, err
	}
	return finishMaterialization(parent, recovery, marker, record)
}

// Explicit clearing preserves the Python API's permission to remove a selected
// private regular file, including legacy nonempty/root-owned files. A captured
// recovery transaction keeps its original intent; a new request cannot broaden it.
func clearExplicitMaterialization(workspace, marker, recovery *os.File, target string) (bool, error) {
	previous, err := readMaterializationRecord(marker)
	if err != nil {
		return false, err
	}
	if previous != nil {
		if previous.Target != target {
			return false, fault.Error("materialized secret marker target mismatch")
		}
		if previous.Quarantine != "" {
			return resumeMaterialization(workspace, recovery, marker, previous)
		}
	}
	parent, err := cleanupParent(workspace, target)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	var stat unix.Stat_t
	err = unix.Fstatat(int(parent.Fd()), path.Base(target), &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, writeMaterializationRecord(marker, nil)
	}
	if err != nil {
		return false, unsafeClear
	}
	record := &materializationRecord{Version: 2, Target: target, Device: uint64(stat.Dev), Inode: stat.Ino, Explicit: true}
	if err := cleanupIntent(parent, recovery, marker, record); err != nil {
		return false, err
	}
	if err := captureMaterialization(parent, recovery, marker, record); err != nil {
		return false, err
	}
	return finishMaterialization(parent, recovery, marker, record)
}
