//go:build windows

package windows

import (
	"context"
	"errors"
	"os"
	"strings"
)

type WindowsConnectionRemovalGuard struct {
	LocalAppData string
}

func (guard WindowsConnectionRemovalGuard) RemoveAllForDistribution(ctx context.Context, distribution string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateDistributionName(distribution); err != nil {
		return err
	}
	localAppData := strings.TrimSpace(guard.LocalAppData)
	if localAppData == "" {
		localAppData = strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	}
	paths, err := ResolveFrontendPaths(localAppData)
	if err != nil {
		return err
	}
	target := joinWindowsPath(paths.ConnectionsRoot, distribution)
	info, err := OSStateFilesystem{}.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Exists {
		return nil
	}
	if !info.Directory || info.Reparse {
		return errors.New("refusing uninstall because frontend-managed connection state is not a real directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("refusing uninstall while frontend-managed connections exist; remove managed connections first")
	}
	return os.Remove(target)
}
