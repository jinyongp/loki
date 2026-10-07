//go:build !windows

package management

import (
	"fmt"
	"os"
	"syscall"
)

func backupOwnership(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", stat.Uid, stat.Gid)
	}
	return "unknown"
}

func copyBackupMetadata(path string, info os.FileInfo) error {
	if os.Geteuid() == 0 {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if err := os.Lchown(path, int(stat.Uid), int(stat.Gid)); err != nil {
				return err
			}
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	return os.Chmod(path, info.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
}
