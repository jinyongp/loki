package project

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
)

var projectIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// RepairMetadata adjusts only central traversal and taskrc permissions. It
// never traverses or changes Taskwarrior's runner-owned database directory.
func RepairMetadata(root string, gid int) (int, error) {
	if os.Geteuid() != 0 {
		return 0, fault.Error("task metadata repair requires root")
	}
	if !filepath.IsAbs(root) || gid < 0 {
		return 0, fault.Error("invalid task metadata repair layout")
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	directory := os.NewFile(uintptr(fd), "central projects")
	defer directory.Close()
	count, visited := 0, 0
	for {
		entries, err := directory.ReadDir(128)
		if err != nil && err != io.EOF {
			return count, err
		}
		for _, entry := range entries {
			visited++
			if visited > 10000 {
				return count, fault.Error("too many central projects for metadata repair")
			}
			if !projectIDPattern.MatchString(entry.Name()) {
				continue
			}
			if err := repairProject(fd, entry.Name(), gid); err != nil {
				return count, err
			}
			count++
		}
		if err == io.EOF {
			return count, nil
		}
	}
}

func repairProject(root int, name string, gid int) error {
	var opened []*os.File
	defer func() {
		for _, file := range opened {
			file.Close()
		}
	}()
	for _, relative := range []string{name, name + "/taskwarrior", name + "/taskwarrior/taskrc"} {
		fd, err := unix.Openat2(root, relative, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), "task metadata")
		opened = append(opened, file)
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if len(opened) < 3 && !info.IsDir() || len(opened) == 3 && (!info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1) {
			return fault.Error("unsafe or incomplete central task metadata")
		}
	}
	for index, file := range opened {
		if err := file.Chown(0, gid); err != nil {
			return err
		}
		mode := os.FileMode(0710)
		if index == 2 {
			mode = 0640
		}
		if err := file.Chmod(mode); err != nil {
			return err
		}
	}
	return nil
}
