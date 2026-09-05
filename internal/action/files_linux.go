package action

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
)

type fileRequest struct {
	Operation string `json:"operation"`
	Target    string `json:"target"`
	UID, GID  uint32
}

type fileResult struct {
	Held    bool   `json:"held"`
	Cleared bool   `json:"cleared"`
	Error   string `json:"error,omitempty"`
}

func relativeFile(name string) bool {
	return name != "" && name != "." && !path.IsAbs(name) && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\x00\\")
}

func openWorkspaceFile(workspace *os.File, target string, flags uint64) (*os.File, error) {
	flags |= unix.O_CLOEXEC
	if flags&unix.O_PATH == 0 {
		flags |= unix.O_NONBLOCK
	}
	fd, err := unix.Openat2(int(workspace.Fd()), target, &unix.OpenHow{Flags: flags, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "action workspace entry"), nil
}

func probeLock(workspace *os.File, target string) (bool, error) {
	file, err := openWorkspaceFile(workspace, target, unix.O_RDONLY)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fault.Error("unable to inspect action runtime lock path")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false, fault.Error("unable to inspect action runtime lock path")
	}
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return true, nil
	}
	if err != nil {
		return false, fault.Error("unable to inspect action runtime lock ownership")
	}
	return false, nil // Closing the descriptor releases our short-lived probe.
}

func claimMaterializationFile(workspace, marker, recovery *os.File, target string) error {
	if _, err := clearMaterialization(workspace, marker, recovery, target); err != nil {
		return err
	}
	parent, err := openWorkspaceFile(workspace, path.Dir(target), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return fault.Error("actions with materialized secrets require an existing target directory")
	}
	defer parent.Close()
	var existing unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), path.Base(target), &existing, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return fault.Error("materialized secret target already exists")
	}
	// An unnamed empty file lets us durably record its identity before linking it
	// into the workspace. A crash before link leaves no orphan placeholder.
	fd, err := unix.Openat(int(parent.Fd()), ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "empty action placeholder")
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		return err
	}
	record := &materializationRecord{Version: 1, Target: target, Device: uint64(stat.Dev), Inode: stat.Ino}
	if err = writeMaterializationRecord(marker, record); err != nil {
		return err
	}
	if err = unix.Linkat(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fd), int(parent.Fd()), path.Base(target), unix.AT_SYMLINK_FOLLOW); err != nil {
		_ = writeMaterializationRecord(marker, nil)
		if errors.Is(err, unix.EEXIST) {
			return fault.Error("materialized secret target already exists")
		}
		return err
	}
	return parent.Sync()
}

// RunFileOperation is a private, finite CLI entrypoint. It receives no secrets.
// The parent supplies pinned workspace/binary/locked-marker descriptors at
// 3/4/5, plus a private recovery directory at 6. Mutations execute under the
// configured runner UID, never as root.
func RunFileOperation(input io.Reader, output io.Writer) error {
	var request fileRequest
	data, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &request) != nil || !relativeFile(request.Target) || request.UID == 0 || os.Getuid() != int(request.UID) || os.Geteuid() != int(request.UID) || os.Getgid() != int(request.GID) {
		return errors.New("invalid action file operation")
	}
	workspace := os.NewFile(3, "pinned workspace")
	defer workspace.Close()
	var result fileResult
	switch request.Operation {
	case "probe":
		result.Held, err = probeLock(workspace, request.Target)
	case "claim", "clear", "clear-explicit":
		marker := os.NewFile(5, "locked materialization marker")
		defer marker.Close()
		recovery := os.NewFile(6, "private materialization recovery")
		defer recovery.Close()
		var stat unix.Stat_t
		err = validateRecoveryDirectory(recovery, workspace, request.UID)
		if err == nil {
			err = unix.Fstat(int(marker.Fd()), &stat)
		}
		if err == nil && (stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Nlink != 1) {
			err = errors.New("unsafe materialization marker")
		}
		if err == nil {
			err = unix.Flock(int(marker.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		}
		if err == nil {
			if request.Operation == "claim" {
				err = claimMaterializationFile(workspace, marker, recovery, request.Target)
			} else if request.Operation == "clear-explicit" {
				result.Cleared, err = clearExplicitMaterialization(workspace, marker, recovery, request.Target)
			} else {
				result.Cleared, err = clearMaterialization(workspace, marker, recovery, request.Target)
			}
		}
	default:
		err = errors.New("invalid action file operation")
	}
	if err != nil {
		result.Error = fault.Public(err)
	}
	return json.NewEncoder(output).Encode(result)
}

func runnerFileOperation(ctx context.Context, layout Layout, workspace, binary, marker *os.File, operation, target string) (fileResult, error) {
	var result fileResult
	request, err := json.Marshal(fileRequest{Operation: operation, Target: target, UID: layout.UID, GID: layout.GID})
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/proc/self/fd/4", "internal", "action-files")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(request)
	cmd.ExtraFiles = []*os.File{workspace, binary}
	if marker != nil {
		recovery, err := pinRecoveryDirectory(layout, workspace)
		if err != nil {
			return result, err
		}
		defer recovery.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, marker, recovery)
	}
	if err := configureRunner(cmd, layout); err != nil {
		return result, err
	}
	data, err := cmd.Output()
	if err != nil {
		return result, errors.New("action file operation failed")
	}
	if len(data) > 4096 || json.Unmarshal(data, &result) != nil {
		return result, errors.New("invalid action file response")
	}
	if result.Error != "" {
		return result, fault.Error(result.Error)
	}
	return result, nil
}

func configureRunner(cmd *exec.Cmd, layout Layout) error {
	if os.Geteuid() == 0 {
		runner, err := user.Lookup(layout.Runner)
		if err != nil || runner.Uid != strconv.FormatUint(uint64(layout.UID), 10) || runner.Gid != strconv.FormatUint(uint64(layout.GID), 10) {
			return errors.New("action runner identity mismatch")
		}
		ids, err := runner.GroupIds()
		if err != nil {
			return err
		}
		groups := make([]uint32, 0, len(ids))
		for _, id := range ids {
			parsed, err := strconv.ParseUint(id, 10, 32)
			if err != nil {
				return err
			}
			groups = append(groups, uint32(parsed))
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: layout.UID, Gid: layout.GID, Groups: groups}}
	}
	return nil
}
