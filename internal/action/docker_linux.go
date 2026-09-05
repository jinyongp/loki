package action

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func validDockerHostRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || strings.ContainsAny(root, "\x00\\") {
		return errors.New("invalid Docker workspace alias")
	}
	for _, protected := range []string{"/workspace", "/usr", "/proc", "/sys", "/dev", "/etc", "/run"} {
		if overlaps(root, protected) {
			return errors.New("Docker workspace alias overlaps a protected mount")
		}
	}
	return nil
}

// Copy the namespace-local read-only environment overlay to the Docker host
// alias before credentials enter the final process environment.
func (p payload) cloneDockerWorkspace(workspace *os.File) error {
	if p.HostWorkspace == "" {
		return nil
	}
	target, err := os.Open(p.HostWorkspace)
	if err != nil {
		return err
	}
	defer target.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(target.Fd()), &stat); err != nil {
		return err
	}
	if stat.Ino != p.WorkspaceInode || uint64(stat.Dev) != p.WorkspaceDevice {
		return errors.New("Docker materialization alias changed")
	}
	mount, err := unix.OpenTree(int(workspace.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE|unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
	if err != nil {
		return err
	}
	defer unix.Close(mount)
	return unix.MoveMount(mount, "", int(target.Fd()), "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH)
}
