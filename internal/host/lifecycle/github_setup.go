package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// GitHubSetupLock serializes the host-owned onboarding session independently
// of lifecycle application, so a browser wait never holds the lifecycle lock.
func (s *FileStore) GitHubSetupLock(ctx context.Context) (*OperationLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := s.githubSetupRoot()
	if err != nil {
		return nil, err
	}
	return AcquireOperationLock(root)
}
func (s *FileStore) githubSetupRoot() (string, error) {
	if s == nil {
		return "", errors.New("host lifecycle store is not configured")
	}
	root := filepath.Join(s.Root, "github-setup")
	if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return "", errors.New("GitHub setup state directory is invalid")
	}
	var stat unix.Stat_t
	if err = unix.Stat(root, &stat); err != nil || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("GitHub setup state owner is invalid")
	}
	return root, nil
}
func (s *FileStore) ReadGitHubSetup(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := s.githubSetupRoot()
	if err != nil {
		return nil, err
	}
	return readManagedPrivateFile(filepath.Join(root, "session.json"), false)
}
func (s *FileStore) WriteGitHubSetup(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.githubSetupRoot()
	if err != nil {
		return err
	}
	return publishManagedPrivateFile(filepath.Join(root, "session.json"), raw)
}
func (s *FileStore) ClearGitHubSetup(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.githubSetupRoot()
	if err != nil {
		return err
	}
	if err = removeIfPresent(filepath.Join(root, "session.json")); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
