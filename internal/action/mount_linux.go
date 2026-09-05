package action

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type materializedMountPayload struct {
	Target                    string
	TargetDevice, TargetInode uint64
	Data                      []byte
}

// injectMaterializedMount runs in the new user/mount namespace. Both the
// temporary source and detached mount are namespace-local. The mount is
// attached to a validated descriptor, never by looking up the target again.
func (p payload) injectMaterializedMount() error {
	if p.Materialized == nil {
		return nil
	}
	m := p.Materialized
	// bubblewrap may enter a second user namespace after mounting /dev/pts.
	// Its existing mount namespace then belongs to the parent user namespace.
	// Own a fresh mount namespace and tmpfs before using descriptor-based mounts.
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return executionStageError{"materialization-namespace", err}
	}
	private, err := os.MkdirTemp("/tmp", ".loki-materialization-")
	if err != nil {
		return err
	}
	defer os.Remove(private)
	if err := unix.Mount("tmpfs", private, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "mode=0700,size=8388608"); err != nil {
		return executionStageError{"materialization-tmpfs", err}
	}
	defer unix.Unmount(private, unix.MNT_DETACH)
	workspace, err := os.Open("/workspace")
	if err != nil {
		return err
	}
	defer workspace.Close()
	target, err := openWorkspaceFile(workspace, strings.TrimPrefix(m.Target, "/workspace/"), unix.O_PATH)
	if err != nil {
		return err
	}
	defer target.Close()
	var destination unix.Stat_t
	if err := unix.Fstat(int(target.Fd()), &destination); err != nil {
		return err
	}
	if uint64(destination.Dev) != m.TargetDevice || destination.Ino != m.TargetInode || destination.Mode&unix.S_IFMT != unix.S_IFREG || destination.Size != 0 || destination.Nlink != 1 || destination.Mode&0777 != 0600 || destination.Uid != p.UID {
		return errors.New("materialization destination identity mismatch")
	}
	file, err := os.CreateTemp(private, "env-")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &filesystem); err != nil {
		return err
	}
	if filesystem.Type != unix.TMPFS_MAGIC {
		return errors.New("materialization source requires private tmpfs")
	}
	if err := file.Chmod(0600); err != nil {
		return err
	}
	mount, err := unix.OpenTree(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
	if err != nil {
		return executionStageError{"materialization-open-tree", err}
	}
	defer unix.Close(mount)
	if err := unix.MountSetattr(mount, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV | unix.MOUNT_ATTR_NOEXEC}); err != nil {
		return executionStageError{"materialization-mount-flags", err}
	}
	if _, err := file.Write(m.Data); err != nil {
		return err
	}
	clear(m.Data)
	if err := unix.MoveMount(mount, "", int(target.Fd()), "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return executionStageError{"materialization-attach", err}
	}
	visible, err := openWorkspaceFile(workspace, strings.TrimPrefix(m.Target, "/workspace/"), unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer visible.Close()
	var original, actual unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &original); err != nil {
		return err
	}
	if err := unix.Fstat(int(visible.Fd()), &actual); err != nil {
		return err
	}
	if err := unix.Fstatfs(int(visible.Fd()), &filesystem); err != nil {
		return err
	}
	if original.Dev != actual.Dev || original.Ino != actual.Ino || filesystem.Flags&unix.ST_RDONLY == 0 {
		return errors.New("materialized mount changed before exec")
	}
	if err := p.cloneDockerWorkspace(workspace); err != nil {
		return executionStageError{"docker-materialization-alias", err}
	}
	// Remove every writable alias and the temporary mount before handing
	// control to the action. The rest of /tmp retains its ordinary behavior.
	if err := os.Remove(file.Name()); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unix.Unmount(private, 0); err != nil {
		return err
	}
	return os.Remove(private)
}
