//go:build windows

package management

import "os"

func backupOwnership(os.FileInfo) string { return "windows" }
func copyBackupMetadata(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	return os.Chmod(path, info.Mode().Perm())
}
