package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"loki/internal/host/lifecycle"
	"loki/internal/platform/safeio"
)

// Local Compose config files are bind mounts: source permissions remain in
// effect in the container. Project only validated public configuration for
// runner-owned consumers, never the platform credential itself.
func (b *Backend) materializeGitHubConfiguration(ctx context.Context, store *lifecycle.FileStore, state lifecycle.ManagedIntegrationToggle) (string, string, error) {
	if !state.Configured || !state.Enabled {
		return "", "", errors.New("GitHub configuration projection requires an enabled integration")
	}
	raw, err := store.ReadManagedIntegrationFile(ctx, lifecycle.ManagedGitHubConfigFile, true)
	if err != nil {
		return "", "", errors.New("managed GitHub public configuration is unavailable")
	}
	if lifecycle.ManagedIntegrationDigest(raw) != state.ConfigSHA256 {
		return "", "", errors.New("managed GitHub public configuration integrity check failed")
	}
	private, err := store.ReadManagedIntegrationFile(ctx, lifecycle.ManagedGitHubCredentialFile, true)
	if err != nil {
		return "", "", errors.New("managed GitHub credential is unavailable")
	}
	defer clear(private)
	if lifecycle.ManagedIntegrationDigest(private) != state.CredentialSHA256 {
		return "", "", errors.New("managed GitHub credential integrity check failed")
	}
	keyPath, err := store.ManagedIntegrationFilePath(lifecycle.ManagedGitHubCredentialFile)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(b.runtimeRoot, "github-public")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", "", errors.New("managed GitHub projection directory must be private and real")
	}
	path := filepath.Join(dir, "github.toml")
	if err = safeio.PublishPrivate(path, raw, true); err != nil {
		return "", "", err
	}
	if err = os.Chmod(path, 0444); err != nil {
		return "", "", err
	}
	return path, keyPath, nil
}
